package openid

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/foxcpp/maddy/framework/config"
	tls2 "github.com/foxcpp/maddy/framework/config/tls"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
)

var (
	_ module.Module          = (*Auth)(nil)
	_ module.OAuthBearerAuth = (*Auth)(nil)
)

const modName = "auth.openid"

type Auth struct {
	instName string

	urls   []string
	tlsCfg tls.Config

	usernameAttribute string

	verifier *oidc.IDTokenVerifier

	log *log.Logger
}

func New(modName, instName string) (module.Module, error) {
	if instName == "" {
		instName = modName
	}
	return &Auth{
		instName: instName,
		log:      &log.Logger{Name: modName},
	}, nil
}

func (a *Auth) Configure(inlineArgs []string, cfg *config.Map) error {
	a.urls = inlineArgs

	var (
		issuer   string
		clientID string
	)
	cfg.Bool("debug", true, false, &a.log.Debug)
	cfg.Custom("tls_client", true, false, func() (interface{}, error) {
		return tls.Config{}, nil
	}, tls2.TLSClientBlock, &a.tlsCfg)
	cfg.Callback("urls", func(m *config.Map, node config.Node) error {
		a.urls = append(a.urls, node.Args...)
		return nil
	})
	cfg.String("issuer", false, false, "", &issuer)
	cfg.String("client_id", false, false, "", &clientID)
	cfg.String("username_attribute", false, false, "sub", &a.usernameAttribute)
	if _, err := cfg.Process(); err != nil {
		return err
	}

	if issuer == "" {
		return fmt.Errorf("%s: issuer not set", a.instName)
	}
	if clientID == "" {
		return fmt.Errorf("%s: client_id not set", a.instName)
	}
	if a.usernameAttribute == "" {
		return fmt.Errorf("%s: username_attribute is empty", a.instName)
	}
	ctx := oidc.ClientContext(context.TODO(), &http.Client{Transport: &http.Transport{TLSClientConfig: &a.tlsCfg}})
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return fmt.Errorf("%s: failed to initialize provider: %w", a.instName, err)
	}
	a.verifier = p.Verifier(&oidc.Config{ClientID: clientID})
	a.log = a.log.Sublogger(issuer)
	return nil
}

func (a *Auth) AuthOAuthBearer(username, token string) error {
	idtoken, err := a.verifier.Verify(context.TODO(), token)
	if err != nil {
		a.log.Error("failed to verify token", err)
		return module.ErrUnknownCredentials
	}
	data := make(map[string]interface{})
	if err := idtoken.Claims(&data); err != nil {
		a.log.Error("failed to parse token claims", err)
		return module.ErrUnknownCredentials
	}
	u, ok := data[a.usernameAttribute].(string)
	if !ok {
		a.log.Error("token missing username attribute", nil)
		return module.ErrUnknownCredentials
	}
	if username != u {
		a.log.Error("username mismatch", fmt.Errorf("expected %s, got %s", u, username))
		return module.ErrUnknownCredentials
	}
	// TODO(adphi): check other fields
	return nil
}

func (a *Auth) Name() string {
	return modName
}

func (a *Auth) InstanceName() string {
	return a.instName
}

func init() {
	module.Register(modName, New)
}
