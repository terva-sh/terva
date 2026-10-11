package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"terva.sh/terva/packages/provider"
)

const awsProfileOutputLimit = 64 << 10

// NewAWSProfileSource resolves a named profile through the AWS CLI.
// The caller must obtain user-layer consent before it uses this source.
// Credentials stay in memory. The source never starts an interactive login.
func NewAWSProfileSource(profile string) provider.BedrockCredentialSource {
	s := &awsProfileSource{profile: profile, now: time.Now, export: exportAWSProfile}
	return s.credentials
}

type awsProfileSource struct {
	mu      sync.Mutex
	profile string
	now     func() time.Time
	export  func(context.Context, string) (provider.BedrockSigningCredentials, time.Time, error)
	keys    provider.BedrockSigningCredentials
	refresh time.Time
	pending chan struct{}
	failure error
	retryAt time.Time
}

func (s *awsProfileSource) credentials(ctx context.Context) (provider.BedrockSigningCredentials, error) {
	for {
		if err := ctx.Err(); err != nil {
			return provider.BedrockSigningCredentials{}, err
		}
		s.mu.Lock()
		if s.failure != nil && s.now().Before(s.retryAt) {
			err := s.failure
			s.mu.Unlock()
			return provider.BedrockSigningCredentials{}, err
		}
		if s.now().Before(s.refresh) {
			keys := s.keys
			s.mu.Unlock()
			return keys, nil
		}
		if pending := s.pending; pending != nil {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				return provider.BedrockSigningCredentials{}, ctx.Err()
			case <-pending:
				continue
			}
		}
		s.pending = make(chan struct{})
		s.mu.Unlock()

		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		keys, expiry, err := s.export(tctx, s.profile)
		cancel()
		s.mu.Lock()
		if err == nil {
			now := s.now()
			if keys.AccessKeyID == "" || keys.SecretAccessKey == "" || !expiry.IsZero() && !expiry.After(now) {
				err = errors.New("AWS profile returned incomplete or expired credentials; run aws sso login with the same --profile")
			} else {
				s.keys = keys
				s.refresh = now.Add(5 * time.Minute)
				if !expiry.IsZero() {
					margin := min(5*time.Minute, expiry.Sub(now)/5)
					s.refresh = minTime(s.refresh, expiry.Add(-margin))
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			s.failure, s.retryAt = err, s.now().Add(5*time.Second)
		} else {
			s.failure, s.retryAt = nil, time.Time{}
		}
		close(s.pending)
		s.pending = nil
		s.mu.Unlock()
		return keys, err
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func exportAWSProfile(ctx context.Context, profile string) (provider.BedrockSigningCredentials, time.Time, error) {
	if strings.TrimSpace(profile) == "" || len(profile) > 256 || strings.HasPrefix(profile, "-") || strings.IndexFunc(profile, unicode.IsControl) >= 0 {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI credentials need an explicit AWS_PROFILE")
	}
	path, err := exec.LookPath("aws")
	if err != nil || !filepath.IsAbs(path) {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI v2 is unavailable; install it in an absolute PATH directory")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI credentials need a user home directory")
	}
	cmd := exec.CommandContext(ctx, path, "configure", "export-credentials", "--profile", profile, "--format", "process", "--no-cli-auto-prompt", "--no-cli-pager")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "AWS_CLI_AUTO_PROMPT=off", "AWS_PAGER=", "AWS_EC2_METADATA_DISABLED=true")
	return readAWSProfileExport(ctx, cmd)
}

func readAWSProfileExport(ctx context.Context, cmd *exec.Cmd) (provider.BedrockSigningCredentials, time.Time, error) {
	var out awsProfileOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.Stdin = nil
	// A helper descendant can retain stdout after the CLI exits or is killed.
	cmd.WaitDelay = 250 * time.Millisecond
	configureAWSProfileProcess(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		return provider.BedrockSigningCredentials{}, time.Time{}, fmt.Errorf("AWS profile refresh: %w", ctx.Err())
	}
	if out.tooLarge {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI credential response exceeds size limit")
	}
	if err != nil {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS profile resolution failed; check the profile and AWS CLI v2, or run aws sso login with the same --profile")
	}
	var result struct {
		Version         int
		AccessKeyID     string `json:"AccessKeyId"`
		SecretAccessKey string
		SessionToken    string
		Expiration      string
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Version != 1 || result.AccessKeyID == "" || result.SecretAccessKey == "" {
		return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI returned invalid credential process JSON")
	}
	var expiry time.Time
	if result.Expiration != "" {
		var err error
		expiry, err = time.Parse(time.RFC3339, result.Expiration)
		if err != nil {
			return provider.BedrockSigningCredentials{}, time.Time{}, errors.New("AWS CLI returned an invalid credential expiration")
		}
	}
	return provider.BedrockSigningCredentials{AccessKeyID: result.AccessKeyID, SecretAccessKey: result.SecretAccessKey, SessionToken: result.SessionToken}, expiry, nil
}

type awsProfileOutput struct {
	bytes.Buffer
	tooLarge bool
}

func (b *awsProfileOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > awsProfileOutputLimit {
		b.tooLarge = true
		return 0, errors.New("AWS CLI output limit")
	}
	return b.Buffer.Write(p)
}
