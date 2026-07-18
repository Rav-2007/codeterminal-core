package editapply

import "testing"

// TestMatchesSecretName is the regression + policy-breadth table for the shared
// Layer-1 name gate. It locks EXISTING behavior (including the 61a9ca7 case-fold
// fix) and adds the Part 3B-2 named gaps. Run against the pre-change secret.go
// it establishes the baseline: existing cases pass, the new true-positives fail
// (proving the gaps). After the patterns are added, all pass.
//
// The gate is basename-only (ResolveSafeTargetPath passes filepath.Base), so
// path-context ("under .ssh/" / ".kube/") is not available — patterns are
// basename globs matched case-insensitively.
func TestMatchesSecretName(t *testing.T) {
	cases := []struct {
		name string
		base string
		want bool
	}{
		// ---- EXISTING behavior — must remain unchanged (baseline: PASS) ----
		{"existing/env", ".env", true},
		{"existing/env-local", ".env.local", true},
		{"existing/pem", "server.pem", true},
		{"existing/key", "private.key", true},
		{"existing/id_rsa", "id_rsa", true},
		{"existing/id_rsa_pub_still_flagged", "id_rsa.pub", true}, // existing id_rsa* flags .pub too; unchanged
		{"existing/p12", "cert.p12", true},
		{"existing/substr_secret", "mysecret.txt", true},
		{"existing/substr_credential", "my_credentials.yaml", true},
		// case-fold fix (61a9ca7) — must stay closed
		{"existing/case_ENV", ".ENV", true},
		{"existing/case_PEM", "server.PEM", true},
		{"existing/case_ID_RSA", "ID_RSA", true},
		{"existing/case_P12", "cert.P12", true},
		// ordinary files — must remain allowed
		{"existing/neg_go", "main.go", false},
		{"existing/neg_py", "server.py", false},
		{"existing/neg_md", "README.md", false},
		{"existing/neg_chunker", "chunker.go", false},

		// ---- NEW true-positives — Part 3B-2 named gaps (baseline: FAIL) ----
		{"new/ed25519", "id_ed25519", true},
		{"new/ecdsa", "id_ecdsa", true},
		{"new/dsa", "id_dsa", true},
		{"new/npmrc", ".npmrc", true},
		{"new/netrc", ".netrc", true},
		{"new/pgpass", ".pgpass", true},
		{"new/kubeconfig_bare", "kubeconfig", true},
		{"new/kubeconfig_prefixed", "admin.kubeconfig", true},
		{"new/kubeconfig_suffixed", "dev-kubeconfig.yaml", true},
		{"new/pfx", "cert.pfx", true},
		{"new/tfstate", "terraform.tfstate", true},
		{"new/tfstate_backup", "terraform.tfstate.backup", true},
		{"new/service_account_json", "my-service-account.json", true},
		{"new/serviceaccount_json", "gcp-serviceaccount-prod.json", true},
		// case-fold must extend to the new patterns too
		{"new/case_ED25519", "ID_ED25519", true},
		{"new/case_NPMRC", ".NPMRC", true},
		{"new/case_TFSTATE", "Terraform.TFState", true},

		// ---- NEW true-negatives — false-positive guards (must stay allowed) ----
		{"neg/ed25519_pub", "id_ed25519.pub", false}, // PUBLIC key is not a secret
		{"neg/ecdsa_pub", "id_ecdsa.pub", false},
		{"neg/dsa_pub", "id_dsa.pub", false},
		{"neg/bare_config", "config", false},        // bare kube config: too broad to catch by basename, by design
		{"neg/package_json", "package.json", false}, // service-account glob must not catch ordinary json
		{"neg/tsconfig_json", "tsconfig.json", false},
		{"neg/app_config_json", "app-config.json", false},
		{"neg/state_go", "state.go", false}, // not *.tfstate
		{"neg/terraform_tf", "terraform.tf", false},
		{"neg/account_js", "account.js", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := MatchesSecretName(c.base); got != c.want {
				t.Errorf("MatchesSecretName(%q) = %v, want %v", c.base, got, c.want)
			}
		})
	}
}
