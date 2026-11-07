package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/axadrn/moin/controlplane/internal/service/mailer"
)

type EmailService struct {
	mailer  mailer.Mailer
	appURL  string
	appName string
}

func NewEmailService(provider mailer.Mailer, appURL, appName string) *EmailService {
	return &EmailService{
		mailer:  provider,
		appURL:  appURL,
		appName: appName,
	}
}

func (s *EmailService) SendForgotPasswordEmail(email, token, name string) error {
	signInURL := fmt.Sprintf("%s/auth/forgot-password/%s", s.appURL, token)
	return s.send("forgot_password", email, forgotPasswordEmailTemplate(signInURL, s.appName))
}

func (s *EmailService) SendMagicLinkEmail(email, token, name string) error {
	return s.send("magic_link", email, magicLinkEmailTemplate(s.MagicLinkURL(token), s.appName))
}

// MagicLinkURL is the sign-in link of a magic link token.
func (s *EmailService) MagicLinkURL(token string) string {
	return fmt.Sprintf("%s/auth/magic-link/%s", s.appURL, token)
}

func (s *EmailService) SendWelcomeEmail(email, name string) error {
	link := fmt.Sprintf("%s/app", s.appURL)
	return s.send("welcome", email, welcomeEmailTemplate(name, link, s.appName))
}

func (s *EmailService) SendWorkspaceInvitationEmail(email, token, workspaceName, inviterName string) error {
	invitationURL := fmt.Sprintf("%s/workspaces/invitations/%s", s.appURL, token)
	return s.send("workspace_invitation", email, workspaceInvitationEmailTemplate(invitationURL, workspaceName, inviterName, s.appName))
}

func (s *EmailService) SendEmailChangeVerification(newEmail, token, userName string) error {
	verifyURL := fmt.Sprintf("%s/auth/verify-email-change/%s", s.appURL, token)
	return s.send("email_change_verification", newEmail, emailChangeVerificationTemplate(userName, verifyURL, s.appName))
}

func (s *EmailService) SendEmailChangeNotification(oldEmail, newEmail, userName string) error {
	return s.send("email_change_notification", oldEmail, emailChangeNotificationTemplate(userName, newEmail, s.appName))
}

func (s *EmailService) SendAccountDeletedEmail(email, name string) error {
	return s.send("account_deleted", email, accountDeletedEmailTemplate(name, s.appName))
}

func (s *EmailService) send(kind, to string, content emailContent) error {
	body, err := renderEmail(content)
	if err != nil {
		return fmt.Errorf("render %s email: %w", kind, err)
	}

	err = s.mailer.Send(context.Background(), mailer.Message{
		To:      to,
		Subject: content.Subject,
		Text:    body.Text,
		HTML:    body.HTML,
	})
	if err == nil {
		slog.Info("email sent", "provider", s.mailer.Name(), "type", kind)
	}
	return err
}
