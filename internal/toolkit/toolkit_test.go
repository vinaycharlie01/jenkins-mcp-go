package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestErrorEnvelope(t *testing.T) {
	err := Wrap(errors.New("boom"), func(error) string { return CodeForbidden })
	var env struct {
		Error ToolError `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &env) != nil || env.Error.Code != CodeForbidden || env.Error.Message != "boom" {
		t.Fatalf("bad envelope: %s", err)
	}
	if got := Wrap(fmt.Errorf("x: %w", context.DeadlineExceeded), nil).(*ToolError).Code; got != CodeTimeout {
		t.Errorf("timeout code = %s", got)
	}
	if got := Wrap(errors.New("x"), nil).(*ToolError).Code; got != CodeUpstream {
		t.Errorf("default code = %s", got)
	}
	if Wrap(nil, nil) != nil {
		t.Error("nil should stay nil")
	}
}

func TestValidators(t *testing.T) {
	if K8sName("name", "my-app.v2") != nil || K8sName("name", "") == nil || K8sName("name", "My_App") == nil || K8sName("name", "../etc") == nil {
		t.Error("K8sName")
	}
	if OptionalNamespace("ns", "") != nil || OptionalNamespace("ns", "a.b") == nil {
		t.Error("OptionalNamespace")
	}
	if Text("t", "ok", 5) != nil || Text("t", "toolong", 5) == nil || Text("t", "a\nb", 5) == nil {
		t.Error("Text")
	}
	if v, err := Limit("l", 0, 20, 100); v != 20 || err != nil {
		t.Error("Limit default")
	}
	if _, err := Limit("l", 101, 20, 100); err == nil {
		t.Error("Limit max")
	}
}
