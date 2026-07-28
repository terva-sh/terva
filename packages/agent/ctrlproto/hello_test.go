package ctrlproto

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// featureConstants maps every Feature* identifier declared in hello.go to its
// wire value, so the source scan below can translate an identifier it finds
// into the string ServerHello must advertise. A gate on an identifier missing
// here fails loudly — extend the map when you declare a feature.
var featureConstants = map[string]string{
	"FeatureImages":          FeatureImages,
	"FeatureResolveEvents":   FeatureResolveEvents,
	"FeatureRestart":         FeatureRestart,
	"FeatureContextTree":     FeatureContextTree,
	"FeatureImageData":       FeatureImageData,
	"FeatureFilesList":       FeatureFilesList,
	"FeatureGenerateTitle":   FeatureGenerateTitle,
	"FeatureWorkspaceEvents": FeatureWorkspaceEvents,
	"FeatureHistoryWindow":   FeatureHistoryWindow,
	"FeatureStage":           FeatureStage,
	"FeatureAttachments":     FeatureAttachments,
	"FeatureSharedFiles":     FeatureSharedFiles,
}

// TestServerHelloAdvertisesServeGatedFeatures pins the invariant that every
// feature this package changes serve behavior on is advertised by the default
// ServerHello. Negotiate is a strict intersection, so a server-behavior
// feature absent from that list is dead on the wire no matter how many
// clients request it. history-window shipped exactly that way: the windowing
// behavior had tests, the advertisement had none.
//
// Presentation features appended conditionally by the composition root
// (FeatureStage, added by the web server only when /stage/ is served) gate
// client rendering, not serve behavior, and are exempt.
func TestServerHelloAdvertisesServeGatedFeatures(t *testing.T) {
	advertised := map[string]bool{}
	for _, f := range ServerHello("terva-test", "0").Features {
		advertised[f] = true
	}
	// FeatureRestart is likewise conditional: appended only under
	// --web-allow-restart, so the default hello must not promise it.
	exempt := map[string]bool{"FeatureStage": true, "FeatureRestart": true}

	gateRE := regexp.MustCompile(`HasFeature\((Feature\w+)\)`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	gated := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range gateRE.FindAllStringSubmatch(string(src), -1) {
			ident := m[1]
			if exempt[ident] {
				continue
			}
			gated[ident] = true
			val, ok := featureConstants[ident]
			if !ok {
				t.Errorf("%s gates on %s, which featureConstants does not know — add it to the map (and to ServerHello if it changes serve behavior)", name, ident)
				continue
			}
			if !advertised[val] {
				t.Errorf("%s gates serve behavior on %s (%q) but ServerHello does not advertise it — the feature can never negotiate and is dead on the wire", name, ident, val)
			}
		}
	}
	// Guard the scan itself: if the regex ever stops matching the gate idiom,
	// this test would pass vacuously. The three known gates must be found.
	for _, ident := range []string{"FeatureImageData", "FeatureWorkspaceEvents", "FeatureHistoryWindow"} {
		if !gated[ident] {
			t.Errorf("source scan no longer finds the %s gate — update gateRE to match how serve code checks features", ident)
		}
	}
}
