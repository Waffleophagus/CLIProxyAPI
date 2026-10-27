package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestServerRejectsIncompleteUnauthenticatedRequestBodiesPromptly(t *testing.T) {
	modes := []struct {
		name           string
		requestLog     bool
		commercialMode bool
	}{
		{name: "request_log_false", requestLog: false, commercialMode: false},
		{name: "request_log_true", requestLog: true, commercialMode: false},
		{name: "commercial_mode_true", requestLog: false, commercialMode: true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			logsDir := t.TempDir()
			rawCfg := fmt.Sprintf(
				"config-version: 8\nserver:\n  commercial-mode: %t\n  max-request-body-bytes: 0\naccess:\n  api-keys:\n    - test-key\nobservability:\n  logs:\n    request-log: %t\n",
				mode.commercialMode,
				mode.requestLog,
			)
			cfg := parseTestServerConfigYAML(t, rawCfg)
			srv := newTestServerWithConfig(t, cfg, WithRequestLoggerFactory(func(c *config.Config, configPath string) logging.RequestLogger {
				return logging.NewFileRequestLogger(c.RequestLog, logsDir, filepath.Dir(configPath), c.ErrorLogsMaxFiles)
			}))

			listener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatalf("listen: %v", errListen)
			}
			defer func() {
				_ = listener.Close()
			}()

			go func() {
				_ = srv.server.Serve(listener)
			}()
			defer func() {
				_ = srv.server.Close()
			}()

			addr := listener.Addr().String()
			cases := []struct {
				name       string
				rawRequest string
				wantPrefix string
			}{
				{
					name: "1MiB boundary connection close",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048576\r\nConnection: close\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "1MiB plus 1 connection close",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048577\r\nConnection: close\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "1MiB boundary keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 1048576\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "small body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "small multipart body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: multipart/form-data; boundary=abc\r\nContent-Length: 128\r\n\r\n--abc\r\n",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "chunked incomplete body",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 401 ",
				},
				{
					name: "options small incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"OPTIONS /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 204 ",
				},
				{
					name: "options chunked incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"OPTIONS /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 204 ",
				},
				{
					name: "healthz small incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"GET /healthz HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 200 ",
				},
				{
					name: "forged websocket upgrade header on healthz keep-alive",
					rawRequest: fmt.Sprintf(
						"GET /healthz HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 200 ",
				},
				{
					name: "trailing slash redirect with incomplete body keep-alive",
					rawRequest: fmt.Sprintf(
						"POST /v1/chat/completions/ HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: 128\r\n\r\n{",
						addr,
					),
					wantPrefix: "HTTP/1.1 307 ",
				},
			}

			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					conn, errDial := net.DialTimeout("tcp", addr, time.Second)
					if errDial != nil {
						t.Fatalf("dial: %v", errDial)
					}
					defer func() {
						_ = conn.Close()
					}()

					if errDeadline := conn.SetDeadline(time.Now().Add(500 * time.Millisecond)); errDeadline != nil {
						t.Fatalf("set deadline: %v", errDeadline)
					}
					if _, errWrite := conn.Write([]byte(tc.rawRequest)); errWrite != nil {
						t.Fatalf("write partial request: %v", errWrite)
					}

					rawResp, errRead := io.ReadAll(conn)
					if errRead != nil {
						t.Fatalf("server did not close connection promptly (err=%v, partial=%q)", errRead, string(rawResp))
					}
					respText := string(rawResp)
					if !strings.HasPrefix(respText, tc.wantPrefix) {
						t.Fatalf("response = %q, want %q prefix", respText, tc.wantPrefix)
					}
					if !strings.Contains(strings.ToLower(respText), "connection: close") {
						t.Fatalf("response missing Connection: close header: %q", respText)
					}
				})
			}

			// Verify authenticated requests and /healthz still work and capture bodies on errors.
			httpClient := &http.Client{Timeout: 2 * time.Second}
			healthResp, errHealth := httpClient.Get("http://" + addr + "/healthz")
			if errHealth != nil {
				t.Fatalf("healthz failed: %v", errHealth)
			}
			_ = healthResp.Body.Close()
			if healthResp.StatusCode != http.StatusOK {
				t.Fatalf("healthz status = %d, want 200", healthResp.StatusCode)
			}

			authPayload := `{"model":"gpt-5","messages":[{"role":"user","content":"authenticated-test-marker"}]}`
			authReq, errNewReq := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(authPayload))
			if errNewReq != nil {
				t.Fatalf("new request: %v", errNewReq)
			}
			authReq.Header.Set("Authorization", "Bearer test-key")
			authReq.Header.Set("Content-Type", "application/json")
			authResp, errDo := httpClient.Do(authReq)
			if errDo != nil {
				t.Fatalf("authenticated request failed: %v", errDo)
			}
			_ = authResp.Body.Close()
			if authResp.StatusCode != http.StatusBadRequest {
				t.Fatalf("authenticated request status = %d, want 400", authResp.StatusCode)
			}

			if !mode.commercialMode {
				entries, errReadDir := os.ReadDir(logsDir)
				if errReadDir != nil {
					t.Fatalf("read logs dir: %v", errReadDir)
				}
				foundBodyInLog := false
				for _, entry := range entries {
					if !strings.HasSuffix(entry.Name(), ".log") {
						continue
					}
					content, errReadFile := os.ReadFile(filepath.Join(logsDir, entry.Name()))
					if errReadFile != nil {
						t.Fatalf("read log file: %v", errReadFile)
					}
					if strings.Contains(string(content), "authenticated-test-marker") {
						foundBodyInLog = true
						break
					}
				}
				if !foundBodyInLog {
					t.Fatalf("expected authenticated request body to be captured in logs")
				}
			}
			if srv.server == nil || srv.server.ReadHeaderTimeout <= 0 {
				t.Fatalf("expected positive http.Server.ReadHeaderTimeout, got %+v", srv.server)
			}
		})
	}
}
