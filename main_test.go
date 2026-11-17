package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/fm39hz/gotomux/internal/daemon"
	"github.com/fm39hz/gotomux/internal/picker"
	"github.com/fm39hz/gotomux/internal/tmux"
	"strings"
	"testing"
	"time"
)

func TestParseReconcileArgs(t *testing.T) {
	opts, err := parseReconcileArgs([]string{"kho-cong", "--hard"})
	if err != nil || opts.Name != "kho-cong" || !opts.Hard || opts.Force {
		t.Fatalf("parsed options=%+v err=%v", opts, err)
	}
	opts, err = parseReconcileArgs([]string{"--force", "--hard", "kho-cong"})
	if err != nil || opts.Name != "kho-cong" || !opts.Hard || !opts.Force {
		t.Fatalf("parsed options=%+v err=%v", opts, err)
	}
	if _, err := parseReconcileArgs([]string{"--force", "kho-cong"}); err == nil {
		t.Fatal("--force without --hard must be rejected")
	}
	if _, err := parseReconcileArgs([]string{"a", "b"}); err == nil {
		t.Fatal("multiple session names must be rejected")
	}
}

func TestHardReconcileConfirmationDefaultsToNo(t *testing.T) {
	for _, answer := range []string{"", "n\n", "no\n", "anything\n"} {
		if acceptHardReconcile(strings.NewReader(answer)) {
			t.Errorf("answer %q unexpectedly approved hard reconcile", answer)
		}
	}
	for _, answer := range []string{"y\n", "yes\n", "YES\n"} {
		if !acceptHardReconcile(strings.NewReader(answer)) {
			t.Errorf("answer %q did not approve hard reconcile", answer)
		}
	}
}

func TestPickerFallsBackFromDaemonRowsWithoutRuntimeContract(t *testing.T) {
	response := daemon.Response{OK: true, Ready: true, SyncedAt: time.Now().Unix(), Sessions: []tmux.LiveSession{{Name: "work", ID: "$1"}}}
	if usablePayload(nil, response) {
		t.Fatal("old daemon row without server identity was accepted")
	}
	response.Sessions[0].ServerKey = "server"
	if !usablePayload(nil, response) {
		t.Fatal("current targetable daemon row was rejected")
	}
}

func TestConnectFailureReportsDaemonOutcomeWithTargetReceipt(t *testing.T) {
	var request bytes.Buffer
	response := json.NewDecoder(strings.NewReader(`{"ok":true}`))
	item := picker.Item{Kind: picker.KindActive, Name: "kho-cong", RuntimeID: "$19", ServerKey: "server-generation"}
	reportConnectResultViaDaemon(json.NewEncoder(&request), response, item, errors.New("client lookup failed"))
	var got daemon.Request
	if err := json.Unmarshal(request.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Cmd != "connect-result" || got.Outcome != "failed" || got.Name != item.Name || got.RuntimeID != item.RuntimeID || got.ServerKey != item.ServerKey || got.Error != "client lookup failed" {
		t.Fatalf("daemon connect result = %+v", got)
	}
}
