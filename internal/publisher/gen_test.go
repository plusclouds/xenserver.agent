package publisher

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestGenerateAPIDoc writes docs/operations.md from the operation catalog.
// Run with: GEN_API_DOC=1 go test ./internal/publisher -run TestGenerateAPIDoc
func TestGenerateAPIDoc(t *testing.T) {
	if os.Getenv("GEN_API_DOC") == "" {
		t.Skip("set GEN_API_DOC=1 to regenerate docs/operations.md")
	}
	names := make([]string, 0, len(operationCatalog))
	for n := range operationCatalog {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`# Agent operations reference

Generated from the agent's own capability catalog (do not edit by hand; run
` + "`GEN_API_DOC=1 go test ./internal/publisher -run TestGenerateAPIDoc`" + `).

## Calling convention

` + "```" + `
POST https://<host>:8443/v1/command
Authorization: Bearer <http.api_key>
Content-Type: application/json

{"v":1,"id":"<your id>","type":"command",
 "payload":{"operation":"xenserver.vm.start","params":{"uuid":"..."}}}
` + "```" + `

Reply: an envelope whose ` + "`payload`" + ` is
` + "`{command_id, status, message?, output?}`" + `.

- ` + "`status`" + `: ` + "`completed`" + ` (ran), ` + "`failed`" + ` (bad parameters or error before running), ` + "`rejected`" + ` (operation not in the host's allowed_operations, or async not supported), ` + "`accepted`" + ` (background job started; HTTP 202).
- State-changing operations return ` + "`output`" + ` = ` + "`{operation, target, success, message, output?, data?, detail?, diagnostics?}`" + `. **Check ` + "`output.success`" + `**: ` + "`status: completed`" + ` only means the agent ran the command. ` + "`data`" + ` holds structured results (for example ` + "`{\"uuid\":\"<new object>\"}`" + `). On failure ` + "`message`" + ` is a plain-language explanation and ` + "`detail`" + ` has ` + "`{command, exit_code, error_code, stderr, stdout}`" + `.
- Read operations return the JSON directly in ` + "`output`" + ` (an array of objects, or one object).
- Long operations accept ` + "`\"async\": true`" + `: the call returns ` + "`202`" + ` with ` + "`output = {job_id, status:\"running\", ...}`" + `. Poll ` + "`agent.job.status {job_id}`" + `; when finished, ` + "`output.result`" + ` holds the operation's normal output. ` + "`agent.job.list`" + ` and ` + "`agent.job.cancel`" + ` also exist.
- Every operation must be listed in the host's ` + "`allowed_operations`" + `. Destructive ones (` + "`*.destroy`" + `, ` + "`unplug`" + `) also need ` + "`confirm: true`" + `.
- ` + "`uuid`/`name`" + ` selectors: most VM operations take ` + "`uuid`" + ` or ` + "`name`" + ` (name must match exactly one VM).

`)
	fmt.Fprintf(&b, "%d operations.\n\n", len(names))
	for _, n := range names {
		s := operationCatalog[n]
		fmt.Fprintf(&b, "### `%s`\n\n%s\n\n", n, s.Description)
		if len(s.Params) == 0 {
			b.WriteString("_No parameters._\n\n")
			continue
		}
		b.WriteString("| param | type | required | description |\n|---|---|---|---|\n")
		for _, p := range s.Params {
			req := "no"
			if p.Required {
				req = "**yes**"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", p.Name, p.Type, req, strings.ReplaceAll(p.Description, "|", "\\|"))
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile("../../docs/operations.md", []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
