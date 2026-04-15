//go:build lk
package maddy

import (
	// Import packages for side-effect of module registration.
	_ "github.com/foxcpp/maddy/internal/auth/ldap"
	_ "github.com/foxcpp/maddy/internal/auth/openid"
	_ "github.com/foxcpp/maddy/internal/check/authorize_sender"
	_ "github.com/foxcpp/maddy/internal/check/command"
	_ "github.com/foxcpp/maddy/internal/check/dkim"
	_ "github.com/foxcpp/maddy/internal/check/dns"
	_ "github.com/foxcpp/maddy/internal/check/dnsbl"
	_ "github.com/foxcpp/maddy/internal/check/milter"
	_ "github.com/foxcpp/maddy/internal/check/requiretls"
	_ "github.com/foxcpp/maddy/internal/check/rspamd"
	_ "github.com/foxcpp/maddy/internal/check/spf"
	_ "github.com/foxcpp/maddy/internal/endpoint/dovecot_sasld"
	_ "github.com/foxcpp/maddy/internal/endpoint/imap"
	_ "github.com/foxcpp/maddy/internal/endpoint/openmetrics"
	_ "github.com/foxcpp/maddy/internal/endpoint/smtp"
	_ "github.com/foxcpp/maddy/internal/imap_filter"
	_ "github.com/foxcpp/maddy/internal/imap_filter/command"
	_ "github.com/foxcpp/maddy/internal/modify"
	_ "github.com/foxcpp/maddy/internal/modify/dkim"
	_ "github.com/foxcpp/maddy/internal/storage/maildir"
	_ "github.com/foxcpp/maddy/internal/table"
	_ "github.com/foxcpp/maddy/internal/target/queue"
	_ "github.com/foxcpp/maddy/internal/target/remote"
	_ "github.com/foxcpp/maddy/internal/target/smtp"
	_ "github.com/foxcpp/maddy/internal/tls"
)