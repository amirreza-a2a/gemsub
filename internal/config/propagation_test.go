package config_test

import (
	"testing"
	"time"

	"gemsub/internal/config"
)

func TestRequiresRestart_RestartRequiredFields(t *testing.T) {
	base := *validTestConfig()

	testCases := []struct {
		name          string
		mutate        func(c *config.Config)
		expectedField string
	}{
		{
			name: "serve.listen modified",
			mutate: func(c *config.Config) {
				c.Serve.Listen = "0.0.0.0:9000"
			},
			expectedField: "serve.listen",
		},
		{
			name: "serve.path modified",
			mutate: func(c *config.Config) {
				c.Serve.Path = "/custom-sub"
			},
			expectedField: "serve.path",
		},
		{
			name: "state_file modified",
			mutate: func(c *config.Config) {
				c.StateFile = "./new_state.json"
			},
			expectedField: "state_file",
		},
		{
			name: "headless modified",
			mutate: func(c *config.Config) {
				c.Headless = !c.Headless
			},
			expectedField: "headless",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modified := base
			tc.mutate(&modified)

			fields := config.RequiresRestart(base, modified)
			if len(fields) != 1 || fields[0] != tc.expectedField {
				t.Fatalf("expected restart fields [%s], got %v", tc.expectedField, fields)
			}
			if !config.IsRestartRequired(base, modified) {
				t.Error("expected IsRestartRequired to return true")
			}
		})
	}
}

func TestRequiresRestart_HotReloadableFieldsDoNotRequireRestart(t *testing.T) {
	base := *validTestConfig()

	testCases := []struct {
		name   string
		mutate func(c *config.Config)
	}{
		{
			name: "fetch_interval",
			mutate: func(c *config.Config) {
				c.FetchIntervalRaw = "5h"
				c.FetchInterval = 5 * time.Hour
			},
		},
		{
			name: "sources",
			mutate: func(c *config.Config) {
				c.Sources = append(c.Sources, "https://new-source.com/sub")
			},
		},
		{
			name: "probe_limit",
			mutate: func(c *config.Config) {
				c.ProbeLimit = 50
			},
		},
		{
			name: "flag_mode",
			mutate: func(c *config.Config) {
				c.FlagMode = "unicode"
			},
		},
		{
			name: "serve.format",
			mutate: func(c *config.Config) {
				c.Serve.Format = "raw"
			},
		},
		{
			name: "test.timeout",
			mutate: func(c *config.Config) {
				c.Test.TimeoutRaw = "20s"
				c.Test.Timeout = 20 * time.Second
			},
		},
		{
			name: "test.concurrency",
			mutate: func(c *config.Config) {
				c.Test.Concurrency = 50
			},
		},
		{
			name: "publishing.enabled",
			mutate: func(c *config.Config) {
				c.Publishing.Enabled = !c.Publishing.Enabled
			},
		},
		{
			name: "publishing.branch",
			mutate: func(c *config.Config) {
				c.Publishing.Branch = "dev"
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			modified := base
			tc.mutate(&modified)

			fields := config.RequiresRestart(base, modified)
			if len(fields) != 0 {
				t.Fatalf("expected 0 restart fields for hot-reloadable update, got %v", fields)
			}
			if config.IsRestartRequired(base, modified) {
				t.Error("expected IsRestartRequired to return false")
			}
		})
	}
}

func TestFieldPolicyMapping(t *testing.T) {
	expectedPolicies := map[string]config.ReloadPolicy{
		"sources":        config.PolicyHotReloadable,
		"fetch_interval": config.PolicyHotReloadable,
		"test":           config.PolicyHotReloadable,
		"publishing":     config.PolicyHotReloadable,
		"probe_limit":    config.PolicyHotReloadable,
		"flag_mode":      config.PolicyHotReloadable,
		"serve.format":   config.PolicyHotReloadable,

		"serve.listen": config.PolicyRestartRequired,
		"serve.path":   config.PolicyRestartRequired,
		"state_file":   config.PolicyRestartRequired,
		"headless":     config.PolicyRestartRequired,
	}

	for field, expected := range expectedPolicies {
		got, ok := config.FieldPolicy[field]
		if !ok {
			t.Errorf("missing policy definition for field %q", field)
			continue
		}
		if got != expected {
			t.Errorf("field %q: expected policy %s, got %s", field, expected, got)
		}
	}
}

func TestService_PendingRestartFields(t *testing.T) {
	cfg := validTestConfig()
	svc, err := config.NewService("", cfg, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Initially no pending restart fields
	if pending := svc.PendingRestartFields(); len(pending) != 0 {
		t.Fatalf("expected 0 pending restart fields initially, got %v", pending)
	}

	// Mutate hot-reloadable field: still no pending restart fields
	err = svc.Update(func(c *config.Config) error {
		c.FetchIntervalRaw = "4h"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if pending := svc.PendingRestartFields(); len(pending) != 0 {
		t.Fatalf("expected 0 pending restart fields after hot-reloadable update, got %v", pending)
	}

	// Mutate restart-required field
	err = svc.Update(func(c *config.Config) error {
		c.Serve.Listen = "127.0.0.1:9999"
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	pending := svc.PendingRestartFields()
	if len(pending) != 1 || pending[0] != "serve.listen" {
		t.Fatalf("expected pending restart fields [serve.listen], got %v", pending)
	}
}
