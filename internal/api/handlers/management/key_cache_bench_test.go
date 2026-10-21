package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func BenchmarkAuthenticateManagementKeyConfiguredHash(b *testing.B) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("bench-secret"), bcrypt.DefaultCost)
	if errHash != nil {
		b.Fatal(errHash)
	}
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = string(hash)
	h := &Handler{cfg: cfg, failedAttempts: make(map[string]*attemptInfo)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if allowed, _, msg := h.AuthenticateManagementKey("127.0.0.1", true, "bench-secret"); !allowed {
			b.Fatal(msg)
		}
	}
}
