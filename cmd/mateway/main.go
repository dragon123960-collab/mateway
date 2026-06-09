package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/cli"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/gateway"
	"github.com/dongping/mateway/internal/runtime"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printRuntimeResponse(resp runtime.Response) {
	messages := channel.OutboundBatch{Reply: resp.Reply, FollowUps: resp.FollowUps}.Messages()
	for i, msg := range messages {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(msg.Text)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printHelp()
		return nil
	}
	switch args[0] {
	case "chat":
		fs := flag.NewFlagSet("mateway chat", flag.ContinueOnError)
		sessionKey := fs.String("session", "", "session key to use")
		cwdSession := fs.Bool("cwd-session", false, "use a session derived from the current working directory")
		classic := fs.Bool("classic", false, "use the classic line-based REPL")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		key := *sessionKey
		if *cwdSession {
			if cwd, err := os.Getwd(); err == nil {
				key = cli.CwdSessionKey(cwd)
			}
		}
		if !*classic && cli.CanRunTUI(os.Stdin, os.Stdout) {
			return cli.RunTUI(context.Background(), cli.TUIOptions{Config: cfg, SessionKey: key, In: os.Stdin, Out: os.Stdout})
		}
		return cli.RunChat(context.Background(), cli.ChatOptions{Config: cfg, SessionKey: key, In: os.Stdin, Out: os.Stdout})
	case "tui":
		fs := flag.NewFlagSet("mateway tui", flag.ContinueOnError)
		sessionKey := fs.String("session", "", "session key to use")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return cli.RunTUI(context.Background(), cli.TUIOptions{Config: cfg, SessionKey: *sessionKey, In: os.Stdin, Out: os.Stdout})
	case "init":
		fs := flag.NewFlagSet("mateway init", flag.ContinueOnError)
		homeFlag := fs.String("home", "", "override MATEWAY_HOME for initialization")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		home := strings.TrimSpace(*homeFlag)
		if home == "" {
			home = config.DefaultHome()
		}
		if err := config.EnsureDefaultConfigFiles(home); err != nil {
			return err
		}
		fmt.Println("initialized", home)
		return nil
	case "ask":
		fs := flag.NewFlagSet("mateway ask", flag.ContinueOnError)
		sessionKey := fs.String("session", "", "session key to use")
		quiet := fs.Bool("quiet", false, "print final answer only")
		jsonOutput := fs.Bool("json", false, "print a JSON response")
		events := fs.Bool("events", false, "print process events as NDJSON")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return cli.RunAsk(context.Background(), cli.AskOptions{
			Config:     cfg,
			Message:    strings.Join(fs.Args(), " "),
			SessionKey: *sessionKey,
			Quiet:      *quiet,
			JSON:       *jsonOutput,
			Events:     *events,
			In:         os.Stdin,
			Out:        os.Stdout,
		})
	case "send":
		fs := flag.NewFlagSet("mateway send", flag.ContinueOnError)
		to := fs.String("to", "", "target in channel:id form")
		uuid := fs.String("uuid", "", "optional idempotency key")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return cli.RunSend(context.Background(), cli.SendOptions{
			Config: cfg,
			To:     *to,
			Text:   strings.Join(fs.Args(), " "),
			UUID:   *uuid,
			Out:    os.Stdout,
		})
	case "fetch-history":
		fs := flag.NewFlagSet("mateway fetch-history", flag.ContinueOnError)
		from := fs.String("from", "", "source in channel:id form")
		sessionKey := fs.String("session", "", "session key to import into")
		limit := fs.Int("limit", 20, "maximum messages to import")
		sinceText := fs.String("since", "24h", "history window such as 24h or 7")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		since, err := cli.ParseSince(*sinceText)
		if err != nil {
			return err
		}
		result, err := cli.RunFetchHistory(context.Background(), cli.FetchHistoryOptions{
			Config:     cfg,
			From:       *from,
			SessionKey: *sessionKey,
			Limit:      *limit,
			Since:      since,
			Out:        os.Stdout,
		})
		if err != nil {
			return err
		}
		cli.PrintFetchHistoryResult(os.Stdout, result)
		return nil
	case "test":
		return runTest(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	case "home":
		return runHome(args[1:])
	case "workspace":
		return runWorkspace(args[1:])
	case "trace":
		fs := flag.NewFlagSet("mateway trace", flag.ContinueOnError)
		events := fs.Bool("events", false, "print process events")
		report := fs.Bool("report", false, "print a human-readable execution report")
		jsonEvents := fs.Bool("json", false, "print process events as NDJSON; requires --events")
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		if len(fs.Args()) != 1 {
			return fmt.Errorf("usage: mateway trace [--events|--report] <trace-jsonl-path>")
		}
		if *report && *events {
			return fmt.Errorf("--report cannot be combined with --events")
		}
		if *events {
			return cli.PrintTraceEventsWithOptions(os.Stdout, fs.Args()[0], cli.TraceEventsOptions{JSON: *jsonEvents})
		}
		if *report {
			return cli.PrintTraceReport(os.Stdout, fs.Args()[0])
		}
		if *jsonEvents {
			return fmt.Errorf("--json requires --events")
		}
		summary, err := runtime.SummarizeTrace(fs.Args()[0])
		if err != nil {
			return err
		}
		fmt.Println("trace:", summary.Path)
		if summary.TraceID != "" {
			fmt.Println("trace_id:", summary.TraceID)
		}
		if summary.SessionKey != "" {
			fmt.Println("session_key:", summary.SessionKey)
		}
		if summary.Channel != "" {
			fmt.Println("channel:", summary.Channel)
		}
		if summary.AccountID != "" {
			fmt.Println("account_id:", summary.AccountID)
		}
		if summary.AgentID != "" {
			fmt.Println("agent_id:", summary.AgentID)
		}
		if summary.TaskID != "" {
			fmt.Println("task_id:", summary.TaskID)
		}
		if summary.MessageID != "" {
			fmt.Println("message_id:", summary.MessageID)
		}
		fmt.Println("events:", summary.Events)
		if !summary.RuntimeDone {
			fmt.Printf("complete: false runtime_done=%t gateway_done=%t\n", summary.RuntimeDone, summary.GatewayDone)
		} else {
			fmt.Println("complete: true")
		}
		fmt.Println("model_ms:", summary.ModelDurationMS)
		fmt.Println("tool_ms:", summary.ToolDurationMS)
		fmt.Println("runtime_ms:", summary.RuntimeDurationMS)
		fmt.Println("reply_ms:", summary.ReplyDurationMS)
		fmt.Println("total_ms:", summary.TotalDurationMS)
		fmt.Println("model_requests:", summary.ModelRequests)
		fmt.Println("input_tokens:", summary.InputTokens)
		fmt.Println("output_tokens:", summary.OutputTokens)
		fmt.Println("total_tokens:", summary.TotalTokens)
		if len(summary.ToolCalls) > 0 {
			fmt.Println("tools:", strings.Join(summary.ToolCalls, ", "))
		}
		return nil
	case "tools":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway tools <list|enable|disable> [--agent <agent_id>] [--verbose] <tool_name>")
		}
		switch args[1] {
		case "list":
			fs := flag.NewFlagSet("mateway tools list", flag.ContinueOnError)
			verbose := fs.Bool("verbose", false, "show descriptions and parallel mode")
			agentID := fs.String("agent", "", "agent profile id")
			if err := fs.Parse(args[2:]); err != nil {
				if err == flag.ErrHelp {
					return nil
				}
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return cli.PrintTools(os.Stdout, cfg, *agentID, *verbose)
		case "enable", "disable":
			fs := flag.NewFlagSet("mateway tools "+args[1], flag.ContinueOnError)
			agentID := fs.String("agent", "", "agent profile id")
			if err := fs.Parse(reorderToolAccessFlags(args[2:])); err != nil {
				if err == flag.ErrHelp {
					return nil
				}
				return err
			}
			if fs.NArg() != 1 {
				return fmt.Errorf("usage: mateway tools %s <tool_name> [--agent <agent_id>]", args[1])
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			var change cli.ToolAccessChange
			if args[1] == "enable" {
				change, err = cli.EnableTool(cfg, *agentID, fs.Arg(0))
			} else {
				change, err = cli.DisableTool(cfg, *agentID, fs.Arg(0))
			}
			if err != nil {
				return err
			}
			cli.PrintToolAccessChange(os.Stdout, change)
			return nil
		default:
			return fmt.Errorf("usage: mateway tools <list|enable|disable> [--agent <agent_id>] [--verbose] <tool_name>")
		}
	case "model":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway model <show|list> [--agent <agent_id>] [--verbose]")
		}
		switch args[1] {
		case "show":
			fs := flag.NewFlagSet("mateway model show", flag.ContinueOnError)
			agentID := fs.String("agent", "", "agent profile id")
			verbose := fs.Bool("verbose", false, "include loaded model endpoints")
			if err := fs.Parse(args[2:]); err != nil {
				if err == flag.ErrHelp {
					return nil
				}
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return cli.PrintModel(os.Stdout, cfg, *agentID, *verbose)
		case "list":
			fs := flag.NewFlagSet("mateway model list", flag.ContinueOnError)
			verbose := fs.Bool("verbose", false, "show endpoint details")
			if err := fs.Parse(args[2:]); err != nil {
				if err == flag.ErrHelp {
					return nil
				}
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			return cli.PrintModels(os.Stdout, cfg, *verbose)
		default:
			return fmt.Errorf("usage: mateway model <show|list> [--agent <agent_id>] [--verbose]")
		}
	case "session":
		return runSession(args[1:])
	case "memory":
		return runMemory(args[1:])
	case "agent-profile":
		return runAgentProfile(args[1:])
	case "agent":
		return runAgent(args[1:])
	case "sandbox":
		return runSandbox(args[1:])
	case "schedule":
		return runSchedule(args[1:])
	case "skill":
		return runSkill(args[1:])
	case "secret":
		return runSecret(args[1:])
	case "channel":
		return runChannel(args[1:])
	case "gateway":
		if len(args) < 2 {
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
		switch args[1] {
		case "serve":
			return serveGateway()
		case "start":
			return gateway.NewServiceManager().Start(context.Background())
		case "restart":
			return gateway.NewServiceManager().Restart(context.Background())
		case "stop":
			return gateway.NewServiceManager().Stop(context.Background())
		case "status":
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			text, err := gateway.NewServiceManager().Status(context.Background(), cfg.App.Home)
			if strings.TrimSpace(text) != "" {
				fmt.Print(text)
			}
			return err
		default:
			return fmt.Errorf("usage: mateway gateway <serve|start|restart|stop|status>")
		}
	case "weixin":
		return runWeixin(args[1:])
	default:
		printHelp()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func loadConfig() (*config.Root, error) {
	return loadConfigFromHome("")
}

func loadConfigFromHome(home string) (*config.Root, error) {
	if strings.TrimSpace(home) == "" {
		home = config.DefaultHome()
	}
	if err := config.EnsureDefaultConfigFiles(home); err != nil {
		return nil, err
	}
	return config.NewLoader(home).Load()
}

func serveGateway() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	rt := runtime.New(cfg)
	return gateway.Serve(context.Background(), gateway.Config{
		Config:  cfg,
		Runtime: rt,
	})
}

func printHelp() {
	fmt.Println(`mateway

Usage:
  mateway init
  mateway chat [--session <session_key>] [--cwd-session] [--classic]
  mateway tui [--session <session_key>]
  mateway ask [--session <session_key>] [--quiet|--json|--events] <message>
  mateway send --to <channel:target> <message>
  mateway fetch-history --from <channel:target> [--session <session_key>] [--limit <n>] [--since <duration>]
  mateway test [--case read-readme|project-index|web-search|write-file] [--message <task>] [--record=false]
  mateway trace [--events] [--json] <trace-jsonl-path>
  mateway tools list [--agent <agent_id>] [--verbose]
  mateway tools enable|disable <tool_name> [--agent <agent_id>]
  mateway model show [--agent <agent_id>] [--verbose]
  mateway model list [--verbose]
  mateway workspace report
  mateway session list
  mateway session show <session_key>
  mateway session archive list <session_key>
  mateway session archive show <session_key> <archive_id>
  mateway memory lint [--root <path>]
  mateway memory index rebuild [--root <path>] [--out <path>]
  mateway memory search [--root <path>] [--scope <scope>] [--type <type>] <query>
  mateway memory proposal create --title <title> --body <body> [--source trace:id]
  mateway memory proposal list
  mateway memory proposal show <proposal_id>
  mateway memory proposal reject <proposal_id> [--reason <text>]
  mateway memory proposal commit <proposal_id>
  mateway agent list
  mateway agent report [agent_id]
  mateway agent lint [agent_id]
  mateway agent create <agent_id> [--name <name>] [--default]
  mateway agent bind --channel <channel> [--account-id <id>] [--peer-id <id>] <agent_id>
  mateway agent unbind --channel <channel> [--account-id <id>] [--peer-id <id>]
  mateway agent-profile proposal list
  mateway agent-profile proposal show <proposal_id>
  mateway agent-profile proposal promote <proposal_id>
  mateway agent-profile proposal reject <proposal_id> [--reason <text>]
  mateway memory distill session <session_key>
  mateway memory distill project close <project_id>
  mateway memory heartbeat lint-index
  mateway memory heartbeat distill
  mateway memory heartbeat learning
  mateway memory heartbeat skill
  mateway memory heartbeat serve [--once] [--interval <duration>]
  mateway memory learning report
  mateway memory report [--root <path>]
  mateway schedule list
  mateway schedule run-due
  mateway schedule serve
  mateway sandbox report
  mateway home report
  mateway skill list
  mateway skill catalog report
  mateway skill search [--all] <query>
  mateway skill install [--name <name>] [--force] <path-or-raw-url>
  mateway skill proposal list
  mateway skill proposal show <proposal_id>
  mateway skill proposal promote <proposal_id>
  mateway skill proposal reject <proposal_id> [--reason <text>]
  mateway skill usage report
  mateway secret set <id> [value]
  mateway secret get <id>
  mateway secret list
  mateway secret delete <id>
  mateway channel list
  mateway weixin login [--timeout <duration>]
  mateway weixin enable [account_id]
  mateway doctor
  mateway gateway <serve|start|restart|stop|status>`)
}
