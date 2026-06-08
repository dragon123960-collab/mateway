package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dongping/mateway/internal/channel/weixin"
	"gopkg.in/yaml.v3"
)

type channelConfigInfo struct {
	ID      string
	Enabled bool
	Path    string
}

func runChannel(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway channel <list>")
	}
	switch args[0] {
	case "list":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		channels, err := listChannelConfigs(filepath.Join(cfg.App.Home, "config", "channels"))
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tENABLED\tCONFIG")
		for _, ch := range channels {
			fmt.Fprintf(tw, "%s\t%t\t%s\n", ch.ID, ch.Enabled, ch.Path)
		}
		return tw.Flush()
	default:
		return fmt.Errorf("usage: mateway channel <list>")
	}
}

func listChannelConfigs(dir string) ([]channelConfigInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read channel config dir: %w", err)
	}
	var channels []channelConfigInfo
	for _, entry := range entries {
		name := entry.Name()
		if shouldSkipRuntimeConfigFile(entry, name) {
			continue
		}
		id := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".yaml")
		path := filepath.Join(dir, name)
		enabled, err := readChannelEnabled(path, id)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channelConfigInfo{ID: id, Enabled: enabled, Path: path})
	}
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].ID < channels[j].ID
	})
	return channels, nil
}

func shouldSkipRuntimeConfigFile(entry os.DirEntry, name string) bool {
	if entry.IsDir() {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" || strings.HasPrefix(lower, "_") || !strings.HasSuffix(lower, ".yaml") {
		return true
	}
	base := strings.TrimSuffix(lower, ".yaml")
	return strings.HasSuffix(base, ".sample") || strings.HasSuffix(base, ".example")
}

func readChannelEnabled(path, id string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]map[string]any
	if err := yaml.Unmarshal(data, &root); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	values := root[id]
	if values == nil {
		return false, nil
	}
	enabled, _ := values["enabled"].(bool)
	return enabled, nil
}

func runWeixin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mateway weixin <login|enable>")
	}
	switch args[0] {
	case "login":
		fs := flag.NewFlagSet("mateway weixin login", flag.ContinueOnError)
		timeout := fs.Duration("timeout", 2*time.Minute, "QR login timeout")
		botType := fs.String("bot-type", "", "optional iLink bot type")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		account, qrURL, err := weixin.Login(context.Background(), cfg.Channels.Weixin, cfg.App.Home, *botType, *timeout, os.Stdout)
		if err != nil {
			return err
		}
		fmt.Println("weixin_account_id:", account.AccountID)
		fmt.Println("weixin_base_url:", account.BaseURL)
		if strings.TrimSpace(qrURL) != "" {
			fmt.Println("qrcode_url:", qrURL)
		}
		fmt.Println("saved_to:", filepath.Join(cfg.App.Home, "run", "weixin", "accounts", account.AccountID+".json"))
		return nil
	case "enable":
		fs := flag.NewFlagSet("mateway weixin enable", flag.ContinueOnError)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		accountID := ""
		if fs.NArg() > 0 {
			accountID = fs.Arg(0)
		}
		account, err := weixin.EnableSavedAccount(cfg.Channels.Weixin, cfg.App.Home, accountID)
		if err != nil {
			return err
		}
		fmt.Println("weixin_enabled:", true)
		fmt.Println("weixin_account_id:", account.AccountID)
		fmt.Println("weixin_base_url:", account.BaseURL)
		fmt.Println("config:", filepath.Join(cfg.App.Home, "config", "channels", "weixin.yaml"))
		return nil
	default:
		return fmt.Errorf("usage: mateway weixin <login|enable>")
	}
}
