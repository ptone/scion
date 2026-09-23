// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"strings"
	"testing"
)

func TestValidateEgressAllow_Accepts(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{""},
		{"api.example.com"},
		{"*.example.com"},
		{"registry.npmjs.org", "pypi.org", "*.pypi.org"},
		{"35.190.0.0/16"},       // external CIDR, no overlap
		{"8.8.8.8"},             // external bare IP
		{"2001:db8::/32"},       // external IPv6 CIDR, no overlap
		{"  api.example.com  "}, // surrounding whitespace trimmed
	}
	for _, entries := range cases {
		if err := ValidateEgressAllow(entries); err != nil {
			t.Errorf("ValidateEgressAllow(%v) = %v, want nil", entries, err)
		}
	}
}

func TestValidateEgressAllow_RejectsCatchAlls(t *testing.T) {
	cases := []string{
		"all", "ALL", "All",
		"*",
		"0.0.0.0/0",
		"::/0",
		"**",
		"*.*",
	}
	for _, entry := range cases {
		err := ValidateEgressAllow([]string{entry})
		if err == nil {
			t.Errorf("ValidateEgressAllow([%q]) = nil, want a catch-all rejection", entry)
			continue
		}
		if !strings.Contains(err.Error(), entry) {
			t.Errorf("ValidateEgressAllow([%q]) error = %v, want it to name the offending entry", entry, err)
		}
	}
}

func TestValidateEgressAllow_RejectsPrivateCIDROverlap(t *testing.T) {
	cases := []string{
		"10.0.0.0/8",
		"10.1.2.0/24", // subset of 10.0.0.0/8
		"172.16.0.0/12",
		"172.20.1.0/24", // subset of 172.16.0.0/12
		"192.168.0.0/16",
		"192.168.1.1", // bare IP inside 192.168.0.0/16
		"100.64.0.0/10",
		"169.254.0.0/16",
		"127.0.0.1",
		"127.0.0.0/8",
		"fc00::/7",
		"fe80::/10",
		"::1",
		"::1/128",
		"8.0.0.0/6", // a supernet that CONTAINS 10.0.0.0/8 — overlap detected either direction
	}
	for _, entry := range cases {
		if err := ValidateEgressAllow([]string{entry}); err == nil {
			t.Errorf("ValidateEgressAllow([%q]) = nil, want a private-range rejection", entry)
		}
	}
}

func TestValidateEgressAllow_RejectsInClusterHostSuffixes(t *testing.T) {
	cases := []string{
		"atenet-router.ate-system.svc",
		"api.ate-system.svc.cluster.local",
		"foo.cluster.local",
		"metadata.internal",
		"ATENET-ROUTER.ATE-SYSTEM.SVC", // case-insensitive
	}
	for _, entry := range cases {
		err := ValidateEgressAllow([]string{entry})
		if err == nil {
			t.Errorf("ValidateEgressAllow([%q]) = nil, want an in-cluster-suffix rejection", entry)
			continue
		}
		if !strings.Contains(err.Error(), entry) {
			t.Errorf("ValidateEgressAllow([%q]) error = %v, want it to name the offending entry", entry, err)
		}
	}
}

func TestValidateEgressAllow_NamesTheOffendingEntryAmongValidOnes(t *testing.T) {
	entries := []string{"api.example.com", "registry.npmjs.org", "10.0.0.0/8", "pypi.org"}
	err := ValidateEgressAllow(entries)
	if err == nil {
		t.Fatal("ValidateEgressAllow() = nil, want an error for the embedded private CIDR")
	}
	if !strings.Contains(err.Error(), "10.0.0.0/8") {
		t.Errorf("error = %v, want it to name 10.0.0.0/8 specifically, not the whole list", err)
	}
}

func TestV1SubstrateConfig_Validate(t *testing.T) {
	var nilCfg *V1SubstrateConfig
	if err := nilCfg.Validate(); err != nil {
		t.Errorf("(*V1SubstrateConfig)(nil).Validate() = %v, want nil", err)
	}

	ok := &V1SubstrateConfig{EgressAllow: []string{"api.example.com"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate() with a clean egress_allow = %v, want nil", err)
	}

	bad := &V1SubstrateConfig{EgressAllow: []string{"all"}}
	err := bad.Validate()
	if err == nil {
		t.Fatal("Validate() with egress_allow: [all] = nil, want an error")
	}
	if !strings.Contains(err.Error(), "egress_allow") {
		t.Errorf("error = %v, want it to name the egress_allow field", err)
	}
}
