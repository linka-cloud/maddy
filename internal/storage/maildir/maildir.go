package maildir

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"path/filepath"
	"strings"

	"github.com/emersion/go-imap"
	sortthread "github.com/emersion/go-imap-sortthread"
	"github.com/emersion/go-imap/backend"
	imapmaildir "github.com/foxcpp/go-imap-maildir"
	maildirpkg "github.com/foxcpp/go-imap-maildir/maildir"
	"github.com/foxcpp/go-imap-maildir/maildir/fs"
	"github.com/foxcpp/go-imap-maildir/maildir/s3"
	imapsql "github.com/foxcpp/go-imap-sql"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/foxcpp/maddy/framework/config"
	modconfig "github.com/foxcpp/maddy/framework/config/module"
	"github.com/foxcpp/maddy/framework/log"
	"github.com/foxcpp/maddy/framework/module"
	"github.com/foxcpp/maddy/internal/authz"
)

var (
	_ module.LifetimeModule    = (*Storage)(nil)
	_ module.ManageableStorage = (*Storage)(nil)
	_ module.DeliveryTarget    = (*Storage)(nil)
	_ module.Table             = (*Storage)(nil)
	_ module.Module            = (*Storage)(nil)
)

type Storage struct {
	instName          string
	modName           string
	path              string
	Log               *log.Logger
	back              *imapmaildir.Backend
	junkMbox          string
	deliveryMap       module.Table
	deliveryNormalize func(context.Context, string) (string, error)
	autoCreateMap     module.Table
	filters           module.IMAPFilter
}

type providerConfig struct {
	Type string
	S3   s3ProviderConfig
}

type s3ProviderConfig struct {
	Endpoint     string
	Secure       bool
	AccessKey    string
	SecretKey    string
	Bucket       string
	Region       string
	ObjectPrefix string
	CredsType    string
}

const (
	credsTypeFileMinio = "file_minio"
	credsTypeFileAWS   = "file_aws"
	credsTypeAccessKey = "access_key"
	credsTypeIAM       = "iam"
	credsTypeDefault   = credsTypeAccessKey
)

func New(modName, instName string) (module.Module, error) {
	if instName == "" {
		instName = modName
	}
	return &Storage{
		instName: instName,
		modName:  modName,
		Log:      &log.Logger{Name: "maildir"},
		junkMbox: "Junk",
	}, nil
}

func (s *Storage) Configure(inlineArgs []string, cfg *config.Map) error {
	if len(inlineArgs) != 0 {
		return fmt.Errorf("%s: expected 0 arguments", s.modName)
	}

	var backendCfg providerConfig
	var deliveryNormalize string
	var appendlimitVal int64 = -1

	cfg.Custom("backend", false, false, func() (interface{}, error) {
		return providerConfig{Type: "fs", S3: s3ProviderConfig{CredsType: credsTypeDefault}}, nil
	}, func(m *config.Map, node config.Node) (interface{}, error) {
		if len(node.Args) != 1 {
			return nil, config.NodeErr(node, "expected exactly one argument")
		}
		backendType := node.Args[0]
		switch backendType {
		case "s3":
			s3Cfg := s3ProviderConfig{CredsType: credsTypeDefault}
			s3Map := config.NewMap(m.Globals, node)
			s3Map.String("endpoint", false, true, "", &s3Cfg.Endpoint)
			s3Map.Bool("secure", false, true, &s3Cfg.Secure)
			s3Map.String("access_key", false, false, "", &s3Cfg.AccessKey)
			s3Map.String("secret_key", false, false, "", &s3Cfg.SecretKey)
			s3Map.String("bucket", false, true, "", &s3Cfg.Bucket)
			s3Map.String("region", false, false, "", &s3Cfg.Region)
			s3Map.String("object_prefix", false, false, "", &s3Cfg.ObjectPrefix)
			s3Map.String("creds", false, false, credsTypeDefault, &s3Cfg.CredsType)
			if _, err := s3Map.Process(); err != nil {
				return nil, err
			}
			return providerConfig{Type: "s3", S3: s3Cfg}, nil
		case "fs":
			if len(node.Children) != 0 {
				return nil, config.NodeErr(node, "fs backend does not accept a block")
			}
			return providerConfig{Type: "fs", S3: s3ProviderConfig{CredsType: credsTypeDefault}}, nil
		default:
			return nil, config.NodeErr(node, "invalid argument, valid values are: %v", []string{"fs", "s3"})
		}
	}, &backendCfg)
	cfg.String("path", false, false, "", &s.path)
	cfg.String("junk_mailbox", false, false, s.junkMbox, &s.junkMbox)
	cfg.Bool("debug", true, false, &s.Log.Debug)
	cfg.Custom("delivery_map", false, false, func() (interface{}, error) {
		return nil, nil
	}, modconfig.TableDirective, &s.deliveryMap)
	cfg.String("delivery_normalize", false, false, "precis_casefold_email", &deliveryNormalize)
	cfg.DataSize("appendlimit", false, false, 32*1024*1024, &appendlimitVal)
	cfg.Custom("imap_filter", false, false, func() (interface{}, error) {
		return nil, nil
	}, func(m *config.Map, node config.Node) (interface{}, error) {
		var filter module.IMAPFilter
		err := modconfig.GroupFromNode("imap_filters", node.Args, node, m.Globals, &filter)
		return filter, err
	}, &s.filters)
	cfg.Custom("autocreate_for", false, false, func() (interface{}, error) {
		return nil, nil
	}, modconfig.TableDirective, &s.autoCreateMap)
	if _, err := cfg.Process(); err != nil {
		return err
	}
	if s.path == "" {
		return errors.New("maildir path is required")
	}
	if !strings.Contains(s.path, "{username}") {
		return errors.New("maildir path must contain {username} placeholder")
	}
	s.path = filepath.Clean(s.path)

	provider, err := s.buildProvider(backendCfg)
	if err != nil {
		return err
	}

	deliveryNormFunc, ok := authz.NormalizeFuncs[deliveryNormalize]
	if !ok {
		return errors.New("maildir: unknown normalization function: " + deliveryNormalize)
	}
	s.deliveryNormalize = func(ctx context.Context, addr string) (string, error) {
		return deliveryNormFunc(addr)
	}
	if s.deliveryMap != nil {
		s.deliveryNormalize = func(ctx context.Context, addr string) (string, error) {
			addr, err := deliveryNormFunc(addr)
			if err != nil {
				return "", err
			}
			mapped, ok, err := s.deliveryMap.Lookup(ctx, addr)
			if err != nil || !ok {
				return "", backend.ErrInvalidCredentials
			}
			return mapped, nil
		}
	}

	backend, err := imapmaildir.New(s.path, provider, nil)
	if err != nil {
		return err
	}
	backend.Log = stdlog.New(logWriter{logger: s.Log, debug: false}, "", 0)
	backend.Debug = stdlog.New(logWriter{logger: s.Log, debug: true}, "", 0)
	if appendlimitVal != -1 {
		if appendlimitVal < 0 {
			return errors.New("maildir: appendlimit must be non-negative")
		}
		if int64(uint32(appendlimitVal)) != appendlimitVal {
			return errors.New("maildir: appendlimit value is too big")
		}
		val := uint32(appendlimitVal)
		if err := backend.SetMessageLimit(&val); err != nil {
			return err
		}
	}
	s.back = backend
	return nil
}

func (s *Storage) GetOrCreateIMAPAcct(username string) (backend.User, error) {
	u, err := s.GetIMAPAcct(username)
	if errors.Is(err, imapsql.ErrUserDoesntExists) {
		if err := s.CreateIMAPAcct(username); err != nil {
			return nil, err
		}
		return s.GetIMAPAcct(username)
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Storage) GetIMAPAcct(username string) (backend.User, error) {
	if s.back == nil {
		return nil, errors.New("maildir storage is not configured")
	}
	u, err := s.back.GetUser(username)
	if err != nil {
		if errors.Is(err, backend.ErrInvalidCredentials) {
			return nil, imapsql.ErrUserDoesntExists
		}
		return nil, err
	}
	return u, nil
}

func (s *Storage) IMAPExtensions() []string {
	return []string{"APPENDLIMIT", "MOVE", "CHILDREN", "SPECIAL-USE", "THREAD=ORDEREDSUBJECT", "SORT"}
}

func (s *Storage) ListIMAPAccts() ([]string, error) {
	return nil, errors.New("maildir: listing accounts is not supported")
}

func (s *Storage) CreateIMAPAcct(username string) error {
	if s.back == nil {
		return errors.New("maildir storage is not configured")
	}
	if _, err := s.GetIMAPAcct(username); err == nil {
		return fmt.Errorf("maildir: account %q already exists", username)
	} else if !errors.Is(err, imapsql.ErrUserDoesntExists) {
		return err
	}
	return s.back.CreateUser(username)
}

func (s *Storage) DeleteIMAPAcct(username string) error {
	if s.back == nil {
		return errors.New("maildir storage is not configured")
	}
	return s.back.DeleteUser(username)
}

func (s *Storage) SupportedThreadAlgorithms() []sortthread.ThreadAlgorithm {
	return []sortthread.ThreadAlgorithm{sortthread.OrderedSubject}
}

func (store *Storage) Login(_ *imap.ConnInfo, username, password string) (backend.User, error) {
	panic("This method should not be called and is added only to satisfy backend.Backend interface")
}

func (s *Storage) StartDelivery(ctx context.Context, msgMeta *module.MsgMetadata, mailFrom string) (module.Delivery, error) {
	if s.back == nil {
		return nil, errors.New("maildir storage is not configured")
	}
	return &delivery{
		store:      s,
		msgMeta:    msgMeta,
		mailFrom:   mailFrom,
		addedRcpts: map[string]addedRcpt{},
		d:          s.back.NewDelivery(),
	}, nil
}

func (s *Storage) Lookup(ctx context.Context, key string) (string, bool, error) {
	user, err := s.GetIMAPAcct(key)
	if err != nil {
		if errors.Is(err, imapsql.ErrUserDoesntExists) {
			return "", false, nil
		}
		return "", false, err
	}
	if err := user.Logout(); err != nil {
		s.Log.Error("logout failed", err, "username", key)
	}
	return "", true, nil
}

func (s *Storage) Name() string {
	return "maildir"
}

func (s *Storage) InstanceName() string {
	return s.instName
}

func (s *Storage) Start() error {
	return nil
}

func (s *Storage) Stop() error {
	if s.back == nil {
		return nil
	}

	err := s.back.Close()
	s.back = nil
	return err
}

func (s *Storage) buildProvider(cfg providerConfig) (maildirpkg.Provider, error) {
	switch cfg.Type {
	case "fs":
		return fs.Provider{}, nil
	case "s3":
		if cfg.S3.Endpoint == "" {
			return nil, errors.New("maildir: s3 backend requires endpoint")
		}
		if cfg.S3.Bucket == "" {
			return nil, errors.New("maildir: s3 backend requires bucket")
		}

		var creds *credentials.Credentials
		switch cfg.S3.CredsType {
		case credsTypeFileMinio:
			creds = credentials.NewFileMinioClient("", "")
		case credsTypeFileAWS:
			creds = credentials.NewFileAWSCredentials("", "")
		case credsTypeIAM:
			creds = credentials.NewIAM("")
		case credsTypeAccessKey:
			creds = credentials.NewStaticV4(cfg.S3.AccessKey, cfg.S3.SecretKey, "")
		default:
			return nil, fmt.Errorf("maildir: unknown s3_creds value %q", cfg.S3.CredsType)
		}

		client, err := minio.New(cfg.S3.Endpoint, &minio.Options{
			Creds:  creds,
			Secure: cfg.S3.Secure,
			Region: cfg.S3.Region,
		})
		if err != nil {
			return nil, fmt.Errorf("maildir: %w", err)
		}

		return s3.NewProvider(client, cfg.S3.Bucket, cfg.S3.ObjectPrefix), nil
	default:
		return nil, fmt.Errorf("maildir: unknown backend %q", cfg.Type)
	}
}

func init() {
	module.Register("storage.maildir", New)
	module.Register("target.maildir", New)
}

type logWriter struct {
	logger *log.Logger
	debug  bool
}

func (w logWriter) Write(p []byte) (int, error) {
	if w.logger == nil {
		return len(p), nil
	}
	msg := strings.TrimRight(string(p), "\n")
	if msg == "" {
		return len(p), nil
	}
	if w.debug {
		w.logger.Debugln(msg)
		return len(p), nil
	}
	w.logger.Println(msg)
	return len(p), nil
}
