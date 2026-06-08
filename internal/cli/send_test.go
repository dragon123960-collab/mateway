package cli

import "testing"

func TestParseSendTargetFeishuDefaultsToChatID(t *testing.T) {
	target, err := parseSendTarget("feishu:oc_123")
	if err != nil {
		t.Fatal(err)
	}
	if target.Channel != "feishu" || target.Kind != "chat_id" || target.ID != "oc_123" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseSendTargetFeishuExplicitKind(t *testing.T) {
	target, err := parseSendTarget("feishu:open_id:ou_123")
	if err != nil {
		t.Fatal(err)
	}
	if target.Channel != "feishu" || target.Kind != "open_id" || target.ID != "ou_123" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseSendTargetFeishuAccountKind(t *testing.T) {
	target, err := parseSendTarget("feishu:ops:chat_id:oc_123")
	if err != nil {
		t.Fatal(err)
	}
	if target.Channel != "feishu" || target.Account != "ops" || target.Kind != "chat_id" || target.ID != "oc_123" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseSendTargetWeixin(t *testing.T) {
	target, err := parseSendTarget("weixin:acct:wxid_123")
	if err != nil {
		t.Fatal(err)
	}
	if target.Channel != "weixin" || target.Account != "acct" || target.ID != "wxid_123" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseSendTargetRejectsMissingID(t *testing.T) {
	if _, err := parseSendTarget("feishu:"); err == nil {
		t.Fatal("expected missing id error")
	}
}
