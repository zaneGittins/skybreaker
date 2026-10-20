// Package k8s lets the broker reverse-proxy the Kubernetes API with a policy
// that speaks verbs, resources and namespaces rather than raw paths. It also
// mints short-lived ServiceAccount tokens, so the broker's only standing
// cluster credential is permission to create tokens for one account.
package k8s

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/zaneGittins/skybreaker/internal/proxy/policy"
)

// Attributes is the apiserver's view of a request (its RequestInfo).
type Attributes struct {
	Verb, APIGroup, APIVersion, Namespace, Resource, Subresource, Name string
	NonResource                                                        bool
	Path                                                               string
}

// Subresources of a namespace object; any other third segment under
// /namespaces/<ns>/ names a namespaced resource.
var namespaceSubresources = map[string]bool{"status": true, "finalize": true}

// Parse mirrors the apiserver's RequestInfoFactory. path must be unescaped
// and is refused if it has dot or empty segments, so the proxy and the
// apiserver can't disagree about which namespace a request touches.
func Parse(method, path string, query url.Values) (Attributes, error) {
	a := Attributes{Path: path}
	parts, err := split(path)
	if err != nil {
		return a, err
	}
	if method == "HEAD" {
		method = "GET"
	}
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return a, fmt.Errorf("unsupported method %s", method)
	}

	var rest []string
	switch {
	case len(parts) >= 3 && parts[0] == "api":
		a.APIVersion, rest = parts[1], parts[2:]
	case len(parts) >= 4 && parts[0] == "apis":
		a.APIGroup, a.APIVersion, rest = parts[1], parts[2], parts[3:]
	default:
		// /api, /api/v1, /apis/<group>/<version>, /version, /openapi/...
		return Attributes{Verb: strings.ToLower(method), NonResource: true, Path: path}, nil
	}

	watch := false
	if rest[0] == "watch" {
		watch, rest = true, rest[1:]
		if len(rest) == 0 {
			return a, errors.New("watch without a resource")
		}
	}
	if rest[0] == "namespaces" && len(rest) > 1 {
		a.Namespace = rest[1]
		if len(rest) > 2 && !namespaceSubresources[rest[2]] {
			rest = rest[2:]
		}
	}
	a.Resource = rest[0]
	if len(rest) > 1 {
		a.Name = rest[1]
	}
	if len(rest) > 2 {
		a.Subresource = rest[2]
	}
	if len(rest) > 3 && a.Subresource != "proxy" {
		return a, fmt.Errorf("unexpected path segments after %s/%s", a.Resource, a.Subresource)
	}

	// Like the apiserver: any value but false or 0 asks for a watch.
	if w := strings.ToLower(query.Get("watch")); !watch && query.Has("watch") && w != "false" && w != "0" {
		watch = true
	}
	switch method {
	case "GET":
		switch {
		case watch:
			a.Verb = "watch"
		case a.Name == "":
			a.Verb = "list"
		default:
			a.Verb = "get"
		}
	case "POST":
		a.Verb = "create"
	case "PUT":
		a.Verb = "update"
	case "PATCH":
		a.Verb = "patch"
	case "DELETE":
		if a.Name == "" {
			a.Verb = "deletecollection"
		} else {
			a.Verb = "delete"
		}
	}
	return a, nil
}

func split(path string) ([]string, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q is not absolute", path)
	}
	trimmed := strings.TrimSuffix(path[1:], "/")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, fmt.Errorf("path %q has an empty or dot segment", path)
		}
		for _, r := range p {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return nil, fmt.Errorf("path %q has a forbidden character", path)
			}
		}
	}
	return parts, nil
}

// Policy is the k8s rule block of a route. Auto and Ask list verbs ("apply"
// means "patch", "*" means every verb). Deny lists resources ("secrets"),
// resource/subresource pairs ("pods/exec"), namespaces ("ns:kube-system") or
// verbs, and wins over everything else.
type Policy struct {
	Auto []string `json:"auto" yaml:"auto"`
	Ask  []string `json:"ask" yaml:"ask"`
	Deny []string `json:"deny" yaml:"deny"`
	// Proxy lists the resources (nodes, pods, services) whose proxy
	// subresource is allowed. A proxy request reaches a kubelet, pod or
	// service with the route's credential, past rules that only see the
	// outer resource, so none is allowed unless listed here.
	Proxy []string `json:"proxy,omitempty" yaml:"proxy,omitempty"`
}

var proxyable = map[string]bool{"nodes": true, "pods": true, "services": true}

var verbs = map[string]bool{
	"get": true, "list": true, "watch": true, "create": true, "update": true,
	"patch": true, "delete": true, "deletecollection": true, "apply": true, "*": true,
}

func (p Policy) Validate() error {
	for _, list := range [][]string{p.Auto, p.Ask} {
		for _, v := range list {
			if !verbs[v] {
				return fmt.Errorf("k8s: %q is not a verb", v)
			}
		}
	}
	for _, d := range p.Deny {
		if err := validDeny(d); err != nil {
			return err
		}
	}
	for _, r := range p.Proxy {
		if !proxyable[r] {
			return fmt.Errorf("k8s: proxy: %q has no proxy subresource (use nodes, pods or services)", r)
		}
	}
	return nil
}

func validDeny(d string) error {
	if verbs[d] {
		return nil
	}
	name := d
	if ns, ok := strings.CutPrefix(d, "ns:"); ok {
		name = ns
	} else if res, sub, ok := strings.Cut(d, "/"); ok {
		if !validName(res) || !validName(sub) {
			return fmt.Errorf("k8s: bad deny entry %q", d)
		}
		return nil
	}
	if !validName(name) {
		return fmt.Errorf("k8s: bad deny entry %q (lowercase resource, resource/subresource, ns:<name> or a verb)", d)
	}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func normVerb(v string) string {
	if v == "apply" {
		return "patch"
	}
	return v
}

func hasVerb(list []string, verb string) bool {
	return slices.ContainsFunc(list, func(v string) bool { return v == "*" || normVerb(v) == verb })
}

// Decide applies deny entries first, then Auto, then Ask; anything else is
// denied. Discovery requests are non-resource GETs and follow "get".
func (p Policy) Decide(a Attributes) (policy.Action, string) {
	if !a.NonResource && a.Subresource == "proxy" && !slices.Contains(p.Proxy, strings.ToLower(a.Resource)) {
		return policy.Deny, a.Resource + "/proxy is not allowed (list it under the route's k8s proxy)"
	}
	for _, d := range p.Deny {
		if deniedBy(d, a) {
			return policy.Deny, "denied by " + d
		}
		// A request with no namespace on a namespaced resource spans every
		// namespace, the denied one included.
		if strings.HasPrefix(d, "ns:") && !a.NonResource && a.Namespace == "" && !isClusterScoped(a) {
			return policy.Deny, "denied by " + d + ": name a namespace"
		}
	}
	switch {
	case hasVerb(p.Auto, a.Verb):
		return policy.Auto, "verb " + a.Verb + " is auto"
	case hasVerb(p.Ask, a.Verb):
		return policy.Ask, "verb " + a.Verb + " needs approval"
	}
	return policy.Deny, "verb " + a.Verb + " is not allowed"
}

// clusterScoped lists the built-in resources that live outside any
// namespace, by resource then API group ("" is the core group): a custom
// resource may share a built-in's name in another group, and is namespaced.
// Anything else without a namespace (including unknown custom resources)
// counts as spanning all namespaces, which fails closed.
var clusterScoped = map[string]string{
	"namespaces": "", "nodes": "", "persistentvolumes": "", "componentstatuses": "",
	"clusterroles": "rbac.authorization.k8s.io", "clusterrolebindings": "rbac.authorization.k8s.io",
	"customresourcedefinitions": "apiextensions.k8s.io", "apiservices": "apiregistration.k8s.io",
	"storageclasses": "storage.k8s.io", "csidrivers": "storage.k8s.io", "csinodes": "storage.k8s.io",
	"volumeattachments": "storage.k8s.io", "priorityclasses": "scheduling.k8s.io",
	"runtimeclasses": "node.k8s.io", "ingressclasses": "networking.k8s.io",
	"mutatingwebhookconfigurations": "admissionregistration.k8s.io", "validatingwebhookconfigurations": "admissionregistration.k8s.io",
	"validatingadmissionpolicies": "admissionregistration.k8s.io", "validatingadmissionpolicybindings": "admissionregistration.k8s.io",
	"certificatesigningrequests": "certificates.k8s.io",
	"flowschemas":                "flowcontrol.apiserver.k8s.io", "prioritylevelconfigurations": "flowcontrol.apiserver.k8s.io",
	"tokenreviews": "authentication.k8s.io", "selfsubjectreviews": "authentication.k8s.io",
	"selfsubjectaccessreviews": "authorization.k8s.io", "selfsubjectrulesreviews": "authorization.k8s.io",
	"subjectaccessreviews": "authorization.k8s.io",
}

func isClusterScoped(a Attributes) bool {
	g, ok := clusterScoped[a.Resource]
	return ok && g == a.APIGroup
}

func deniedBy(d string, a Attributes) bool {
	if verbs[d] {
		return d == "*" || normVerb(d) == a.Verb
	}
	if a.NonResource {
		return false
	}
	if ns, ok := strings.CutPrefix(d, "ns:"); ok {
		return strings.EqualFold(a.Namespace, ns)
	}
	if res, sub, ok := strings.Cut(d, "/"); ok {
		return strings.EqualFold(a.Resource, res) && strings.EqualFold(a.Subresource, sub)
	}
	return strings.EqualFold(a.Resource, d)
}
