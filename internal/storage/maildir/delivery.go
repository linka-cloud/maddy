package maildir

import (
	"context"
	"errors"
	"runtime/trace"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend"
	"github.com/emersion/go-message/textproto"
	"github.com/emersion/go-smtp"
	imapmaildir "github.com/foxcpp/go-imap-maildir"

	"github.com/foxcpp/maddy/framework/buffer"
	"github.com/foxcpp/maddy/framework/exterrors"
	"github.com/foxcpp/maddy/framework/module"
	"github.com/foxcpp/maddy/internal/target"
)

type delivery struct {
	store      *Storage
	msgMeta    *module.MsgMetadata
	d          imapmaildir.Delivery
	mailFrom   string
	addedRcpts map[string]addedRcpt
}

type addedRcpt struct {
	rcptTo string
}

func userDoesNotExist(actual error) error {
	return &exterrors.SMTPError{
		Code:         501,
		EnhancedCode: exterrors.EnhancedCode{5, 1, 1},
		Message:      "User does not exist",
		TargetName:   "maildir",
		Err:          actual,
	}
}

func (d *delivery) AddRcpt(ctx context.Context, rcptTo string, _ smtp.RcptOptions) error {
	defer trace.StartRegion(ctx, "maildir/AddRcpt").End()

	if rcptTo == "" {
		return errors.New("maildir: empty recipient")
	}

	accountName, err := d.store.deliveryNormalize(ctx, rcptTo)
	if err != nil {
		return userDoesNotExist(err)
	}

	if _, ok := d.addedRcpts[accountName]; ok {
		return nil
	}

	if err := d.autoCreateAccount(ctx, accountName); err != nil {
		return err
	}

	userHeader := textproto.Header{}
	userHeader.Add("Delivered-To", accountName)
	if err := d.d.AddRcpt(accountName, userHeader); err != nil {
		if errors.Is(err, backend.ErrNoSuchMailbox) || errors.Is(err, backend.ErrInvalidCredentials) {
			return userDoesNotExist(err)
		}
		return err
	}

	d.addedRcpts[accountName] = addedRcpt{rcptTo: rcptTo}
	return nil
}

func (d *delivery) Body(ctx context.Context, header textproto.Header, body buffer.Buffer) error {
	defer trace.StartRegion(ctx, "maildir/Body").End()

	if d.msgMeta != nil && !d.msgMeta.Quarantine && d.store.filters != nil {
		for accountName, rcpt := range d.addedRcpts {
			folder, flags, err := d.store.filters.IMAPFilter(accountName, rcpt.rcptTo, d.msgMeta, header, body)
			if err != nil {
				d.store.Log.Error("IMAPFilter failed", err, "rcpt", accountName)
				continue
			}
			d.d.UserMailbox(accountName, folder, flags)
		}
	}

	if d.msgMeta != nil && d.msgMeta.Quarantine {
		if err := d.d.SpecialMailbox(imap.JunkAttr, d.store.junkMbox); err != nil {
			return err
		}
	}

	header = header.Copy()
	header.Add("Return-Path", "<"+target.SanitizeForHeader(d.mailFrom)+">")
	return d.d.BodyParsed(header, body.Len(), body)
}

func (d *delivery) Abort(ctx context.Context) error {
	defer trace.StartRegion(ctx, "maildir/Abort").End()

	return d.d.Abort()
}

func (d *delivery) Commit(ctx context.Context) error {
	defer trace.StartRegion(ctx, "maildir/Commit").End()

	return d.d.Commit()
}

func (d *delivery) autoCreateAccount(ctx context.Context, accountName string) error {
	if d.store.autoCreateMap == nil {
		return nil
	}

	_, ok, err := d.store.Lookup(ctx, accountName)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}

	_, ok, err = d.store.autoCreateMap.Lookup(ctx, accountName)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	_, err = d.store.GetOrCreateIMAPAcct(accountName)
	if err != nil {
		return err
	}
	return nil
}
