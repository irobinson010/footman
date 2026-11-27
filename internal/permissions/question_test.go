package permissions_test

import (
	"encoding/json"
	"testing"

	"github.com/irobinson010/footman/internal/permissions"
)

func TestQuestion(t *testing.T) {
	for _, c := range []struct{ name, args, want string }{
		{"open_url", `{"url":"https://www.example.com/a/b?q=1"}`, "Shall I open example.com?"},
		{"open_url", `{"url":"example.org"}`, "Shall I open example.org?"},
		{"open_app", `{"app":"Firefox"}`, "Shall I open Firefox?"},
		{"quick_action", `{"action":"volume down"}`, "Shall I do this: volume down?"},
		{"open", `{"command":"open youtube."}`, "Shall I open youtube?"},
		{"delete_file", `{"path":"/tmp/x"}`, "Shall I go ahead with delete file?"},
		{"send_email", ``, "Shall I go ahead with send email?"},
	} {
		if got := permissions.Question(c.name, json.RawMessage(c.args)); got != c.want {
			t.Errorf("Question(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}
