// Package planner decides which certificate orders are due.
package planner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/manifest"
)

// Order is one due certbot order.
type Order struct {
	DomainName       string   `json:"domain"`
	ProfileName      string   `json:"profile"`
	PreferredProfile string   `json:"preferred_profile,omitempty"`
	SlotDate         string   `json:"slot_date"`
	Slot             int      `json:"slot"`
	SlotTime         string   `json:"slot_time"`
	CertName         string   `json:"cert_name"`
	Identifiers      []string `json:"identifiers"`
}

// Due returns all issuance slots due at now and absent from the manifest.
func Due(cfg config.Config, current manifest.Manifest, now time.Time) []Order {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	slotDate := day.Format("20060102")
	var out []Order

	for _, domain := range cfg.Domains {
		for _, profile := range domain.Profiles {
			interval := 24 * time.Hour / time.Duration(profile.PerDay)
			for slot := 0; slot < profile.PerDay; slot++ {
				slotTime := day.Add(time.Duration(slot) * interval)
				if now.Before(slotTime) {
					continue
				}
				if current.HasSlot(domain.Name, profile.Name, slotDate, slot) {
					continue
				}
				out = append(out, orderFor(domain, profile, slotDate, slot, slotTime))
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].DomainName != out[j].DomainName {
			return out[i].DomainName < out[j].DomainName
		}
		if out[i].ProfileName != out[j].ProfileName {
			return out[i].ProfileName < out[j].ProfileName
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

func orderFor(domain config.DomainConfig, profile config.ProfileConfig, slotDate string, slot int, slotTime time.Time) Order {
	slotText := fmt.Sprintf("%02d", slot)
	identifiers := append([]string(nil), domain.Identifiers...)
	if domain.UniqueSANTemplate != "" {
		identifiers = append(identifiers, renderTemplate(domain.UniqueSANTemplate, domain.Name, profile.Name, slotDate, slotText, slotTime))
	}
	identifiers = dedupe(identifiers)
	return Order{
		DomainName:       domain.Name,
		ProfileName:      profile.Name,
		PreferredProfile: profile.PreferredProfile,
		SlotDate:         slotDate,
		Slot:             slot,
		SlotTime:         slotTime.Format(time.RFC3339),
		CertName:         certName(domain.Name, profile.Name, slotDate, slotText),
		Identifiers:      identifiers,
	}
}

func renderTemplate(template, domain, profile, date, slot string, slotTime time.Time) string {
	replacer := strings.NewReplacer(
		"{domain}", domain,
		"{profile}", strings.ToLower(profile),
		"{date}", date,
		"{slot}", slot,
		"{unix}", fmt.Sprintf("%d", slotTime.Unix()),
	)
	return replacer.Replace(template)
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

var certNameCleaner = regexp.MustCompile(`[^a-z0-9]+`)

func certName(domain, profile, date, slot string) string {
	raw := strings.ToLower(domain + "-" + profile + "-" + date + "-" + slot)
	cleaned := certNameCleaner.ReplaceAllString(raw, "-")
	cleaned = strings.Trim(cleaned, "-")
	return "ub-certmint-" + cleaned
}
