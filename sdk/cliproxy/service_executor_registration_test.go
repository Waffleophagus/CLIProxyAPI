package cliproxy

import (
	"context"
	"net/http"
	"testing"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type serviceTestStubExecutor struct{}
type serviceTestSDKExecutor struct{ serviceTestStubExecutor }

func (serviceTestSDKExecutor) Identifier() string { return "sdk-provider" }

func (serviceTestStubExecutor) Identifier() string {
	return "stub-provider"
}

func (serviceTestStubExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestStubExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (serviceTestStubExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (serviceTestStubExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestStubExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRegisterAvailableExecutorsPreservesSDKExecutorUnlessForced(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	custom := serviceTestSDKExecutor{}
	manager.RegisterExecutor(custom)
	auth := &coreauth.Auth{ID: "private-auth", Provider: custom.Identifier()}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	service := &Service{cfg: &config.Config{}, coreManager: manager}

	service.registerAvailableExecutors(context.Background(), executorRegistrationOptions{auths: manager.List()})
	got, ok := manager.Executor(custom.Identifier())
	if !ok || got != custom {
		t.Fatalf("unforced registration replaced SDK executor with %T", got)
	}

	service.registerExecutorForAuth(auth, true)
	got, ok = manager.Executor(custom.Identifier())
	if !ok {
		t.Fatal("forced registration removed executor")
	}
	if _, replaced := got.(*runtimeexecutor.OpenAICompatExecutor); !replaced {
		t.Fatalf("forced registration kept %T, want *executor.OpenAICompatExecutor", got)
	}
}
