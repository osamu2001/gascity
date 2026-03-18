package workspacesvc

import (
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/supervisor"
)

func derivePublishedURL(pubCfg supervisor.PublicationConfig, workspaceName string, svc config.Service) (string, string) {
	visibility := svc.PublicationVisibilityOrDefault()
	if visibility == "private" {
		return "", ""
	}
	if pubCfg.ProviderOrDefault() == "" {
		return "", "publication_requires_supervisor"
	}
	if pubCfg.ProviderOrDefault() != "hosted" {
		return "", "publication_provider_unsupported"
	}
	tenantSlug := normalizeRouteLabel(pubCfg.TenantSlug, "")
	if tenantSlug == "" {
		return "", "publication_tenant_slug_missing"
	}
	baseDomain := pubCfg.BaseDomainForVisibility(visibility)
	if baseDomain == "" {
		switch visibility {
		case "public":
			return "", "publication_public_base_domain_missing"
		case "tenant":
			return "", "publication_tenant_base_domain_missing"
		default:
			return "", "publication_domain_missing"
		}
	}
	if visibility == "tenant" && strings.TrimSpace(pubCfg.TenantAuth.PolicyRef) == "" {
		return "", "publication_tenant_auth_policy_missing"
	}
	serviceLabel := normalizeRouteLabel(svc.PublicationHostnameOrDefault(), "service")
	workspaceLabel := normalizeRouteLabel(workspaceName, "workspace")
	hash := publicationHash(serviceLabel, workspaceLabel, tenantSlug, visibility)
	host := fmt.Sprintf("%s--%s--%s--%s.%s", serviceLabel, workspaceLabel, tenantSlug, hash, baseDomain)
	if len(host) > 253 {
		return "", "publication_hostname_too_long"
	}
	return "https://" + host, "route_active"
}

func normalizeRouteLabel(value, fallback string) string {
	return config.NormalizePublicationLabel(value, fallback)
}

func publicationHash(serviceLabel, workspaceLabel, tenantLabel, visibility string) string {
	// Keep the hash input explicit and unambiguous; labels are normalized to
	// ASCII route components before hashing, so the separators are structural.
	sum := sha256.Sum256([]byte(strings.Join([]string{
		serviceLabel,
		workspaceLabel,
		tenantLabel,
		visibility,
	}, "\x00")))
	encoded := base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	if len(encoded) < 8 {
		return strings.ToLower(encoded)
	}
	return strings.ToLower(encoded[:8])
}
