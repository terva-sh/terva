package modelreg

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/provider"
)

// ResolveRef resolves a model reference a user typed or picked: provider/id,
// or a bare model id. Pass the provider the user is on as prefer, or "" when
// there is none.
//
// provider/id is read first, cut at the first slash: the provider before it,
// and the id under that provider after it. That is the form favorites and
// hidden models already key on, and it reaches every catalog row, so
// openrouter/deepseek/deepseek-v4.1-flash names the OpenRouter id. When no
// provider by that name lists the rest, the whole reference is a bare id. A
// bare id resolves under prefer first, then to the first provider that lists
// it.
//
// warning is non-empty when the reference also matches as a whole id under
// another provider. The caller shows it, so a user whose reference changed
// meaning learns how to write the other one.
func ResolveRef(ref, prefer string) (m provider.Model, warning string, err error) {
	return resolveRef(reg, ref, prefer)
}

// QualifiedRef is the provider/id form of m, which ResolveRef reads back to m.
func QualifiedRef(m provider.Model) string { return QualifiedRefOf(m.Provider, m.ID) }

// QualifiedRefOf is the provider/id form of a provider and an id.
func QualifiedRefOf(providerID, id string) string { return providerID + "/" + id }

// 🔑 Split before exact. Gateways list ids that read as provider/id: on
// 2026-10-02, 55 vercel-ai-gateway ids such as openai/gpt-5.2-pro were also
// the qualified form of a direct-provider row. Reading the whole id first left
// those 55 direct rows with no text form that reached them. Reading the split
// first leaves every row reachable, because a gateway id still has its own
// qualified form, vercel-ai-gateway/openai/gpt-5.2-pro. The cost is that a
// bare gateway id written before this moves to the direct provider, which is
// what the warning is for.
func resolveRef(cat provider.ModelCatalog, ref, prefer string) (provider.Model, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return provider.Model{}, "", fmt.Errorf("empty model reference")
	}
	if prov, id, ok := strings.Cut(ref, "/"); ok && prov != "" && id != "" {
		if m, err := cat.FindModel(prov, id); err == nil {
			warning := ""
			if other, err := cat.FindModel("", ref); err == nil {
				warning = fmt.Sprintf("model %q reads as %s; for the %s model with that id, write %q",
					ref, QualifiedRef(m), other.Provider, QualifiedRef(other))
			}
			return m, warning, nil
		}
	}
	if prefer != "" {
		if m, err := cat.FindModel(prefer, ref); err == nil {
			return m, "", nil
		}
	}
	if m, err := cat.FindModel("", ref); err == nil {
		return m, "", nil
	}
	return provider.Model{}, "", fmt.Errorf("unknown model %q", ref)
}
