package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestImageMCPCodexCanvas(t *testing.T) {
	codexSignedIn(t)
	const prompt = "A centered blue ceramic mug on a plain white background."
	const portrait = "1080 pixels wide and 1920 pixels high"
	cases := []struct {
		name, size, wantHint, wantSize string
		model                          string
		mcp, stdio, edit               bool
	}{
		{name: "HTTP MCP generation", mcp: true, size: "1080x1920", wantHint: portrait},
		{name: "HTTP MCP edit", mcp: true, edit: true, size: "9:16", wantHint: "width-to-height aspect ratio of 9:16"},
		{name: "stdio MCP generation", stdio: true, size: "1080×1920", wantHint: portrait},
		{name: "stdio MCP edit", stdio: true, edit: true, size: "9:16", wantHint: "width-to-height aspect ratio of 9:16"},
		{name: "automatic", mcp: true, size: "auto", wantSize: "auto"},
		{name: "omitted", mcp: true},
		{name: "unrecognized", mcp: true, size: "unrecognized", wantSize: "unrecognized"},
		{name: "zero width", mcp: true, size: "0x1920", wantSize: "0x1920"},
		{name: "direct API", size: "1080x1920", wantSize: "1080x1920"},
		{name: "remote Codex", mcp: true, model: "office/codex/gpt-image-2", size: "1080x1920", wantHint: portrait},
		{name: "remote API key", mcp: true, model: "office/openai/gpt-image-2", size: "1080x1920", wantSize: "1080x1920"},
	}
	type ask struct {
		Path string
		Body struct {
			Prompt, Size string
			Images       []map[string]string
		}
	}
	asks := make(chan ask, len(cases))
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/images/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var a ask
		a.Path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&a.Body); err != nil {
			t.Error(err)
		}
		asks <- a
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)}}})
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	if err := provider.Save(provider.Provider{
		ID: "office", Preset: provider.RemoteMagpiePreset, Chat: up.URL + "/v1", Key: "fixture-key",
		Models: []string{"codex/gpt-image-2", "openai/gpt-image-2"},
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.imageStore.running.Wait)
	h := lanGuard(s.Handler())
	imageURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"prompt": prompt, "model": "codex/gpt-image-2"}
			if tc.size != "" {
				args["size"] = tc.size
			}
			endpoint := "/images/generations"
			if tc.edit {
				endpoint = "/images/edits"
			}
			if tc.mcp {
				if tc.model != "" {
					args["model"] = tc.model
				} else {
					delete(args, "model") // exercise the default subscription too
				}
				if tc.edit {
					args["reference_images"] = []string{imageURL}
				}
				m := readImageReply(t, imagePost(h, "127.0.0.1:5000", imageToolCall(GenerateImageTool, args)))
				if m.Result.IsError {
					t.Fatalf("tool call failed: %+v", m.Result.Content)
				}
			} else {
				if tc.edit {
					args["images"] = []map[string]string{{"image_url": imageURL}}
				}
				body, _ := json.Marshal(args)
				r := httptest.NewRequest(http.MethodPost, "/v1"+endpoint, strings.NewReader(string(body)))
				r.RemoteAddr = "127.0.0.1:5000"
				r.Host = "127.0.0.1:3425"
				r.Header.Set("Content-Type", "application/json")
				if tc.stdio {
					r.Header.Set("User-Agent", "claude-code/2.1.0 "+DrawAgent)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
			}
			select {
			case got := <-asks:
				if !strings.HasSuffix(got.Path, endpoint) || (len(got.Body.Images) > 0) != tc.edit {
					t.Errorf("wrong upstream request: %s, %d references", got.Path, len(got.Body.Images))
				}
				if tc.wantHint == "" && got.Body.Prompt != prompt || tc.wantHint != "" && (!strings.HasPrefix(got.Body.Prompt, prompt) || !strings.Contains(got.Body.Prompt, tc.wantHint)) {
					t.Errorf("upstream prompt = %q, want original prompt and canvas hint %q", got.Body.Prompt, tc.wantHint)
				}
				if got.Body.Size != tc.wantSize {
					t.Errorf("upstream size = %q, want %q", got.Body.Size, tc.wantSize)
				}
			default:
				t.Fatal("no upstream image request")
			}
		})
	}
}
