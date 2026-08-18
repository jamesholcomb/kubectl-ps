// Command kubectl-ps is a kubectl plugin that prints ps-style
// resource tables for pods, nodes and namespaces.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

/* ---------- configuration ---------- */

type columnCfg struct {
	mem, cpu bool
	metrics  []rune // order for headers and rows
	showNode bool   // pods
	total    bool   // TOTAL row
}

func isMetric(ch rune) bool   { return strings.ContainsRune("rlupft", ch) }
func isNodeOnly(ch rune) bool { return ch == 'f' || ch == 't' }

/* ---------- entry point ---------- */

func main() {
	scope, flagsStr, opts, nsList, statusTokens, err := parseArgs(os.Args[1:])
	if err != nil {
		usage(err.Error())
	}

	/* -------- pods scope: -s phase filter (default Succeeded) -------- */
	var phases []string
	if scope == "pods" {
		phases, err = canonicalStatuses(defaultStatuses(statusTokens))
		if err != nil {
			usage(err.Error())
		}
	}

	/* -------- parse scope / flags -------- */
	cfg := parseFlags(flagsStr, scope)
	famOrder, metricPrimary := detectSort(flagsStr)

	/* -------- option variables -------- */
	allNS, reverse := false, false
	units := unitHuman

	/* -------- handle options -------- */
	for _, opt := range opts {
		switch opt {
		case "-A":
			allNS = true
		case "-r":
			reverse = true
		case "-h":
			units = unitHuman
		case "-m":
			units = unitMi
		case "-g":
			units = unitGi
		case "-b":
			units = unitBytes
		case "-t", "--total":
			cfg.total = true
		case "--help":
			usage("")
		default:
			usage("unknown option " + opt)
		}
	}

	/* -------- kube config -------- */
	restCfg, curNS := mustBuildConfig()
	client := mustClient(restCfg)

	/* -------- metrics client (if needed) -------- */
	var mClient *metricsclient.Clientset
	if containsRune(cfg.metrics, 'u') || containsRune(cfg.metrics, 'f') {
		if mc, err := metricsclient.NewForConfig(restCfg); err == nil {
			mClient = mc
		} else {
			log.Printf("metrics-server unavailable: %v", err)
			cfg.metrics = filterRunes(cfg.metrics,
				func(r rune) bool { return r != 'u' && r != 'p' })
		}
	}

	/* -------- dispatch by scope -------- */
	switch scope {
	case "pods":
		runPods(client, mClient, curNS, nsList, allNS, phases,
			cfg, famOrder, metricPrimary, reverse, units)
	case "nodes":
		runNodes(client, mClient,
			cfg, famOrder, metricPrimary, reverse, units)
	case "namespaces":
		runNamespaces(client, mClient,
			cfg, famOrder, metricPrimary, reverse, units)
	}
}

/* ---------- flag parsing ---------- */

// parseArgs splits command-line arguments (excluding the program name)
// into the canonical scope, the metric-flags string, the option tokens, the
// namespace list and the raw pod-status tokens. -n and -s each consume one
// or more following non-option tokens (space-separated, up to the next option).
func parseArgs(args []string) (scope, flags string, opts, nsList, statuses []string, err error) {
	if len(args) == 0 {
		return "", "", nil, nil, nil, errors.New("missing scope")
	}

	scopeArg := args[0]

	for i := 1; i < len(args); i++ {
		tok := args[i]

		/* option (starts with “-”) */
		if strings.HasPrefix(tok, "-") {

			/* -n / -s take one or more value tokens (up to next option) */
			if tok == "-n" || tok == "-s" {
				count := 0
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					if tok == "-n" {
						nsList = append(nsList, args[i+1])
					} else {
						statuses = append(statuses, args[i+1])
					}
					i++
					count++
				}
				if count == 0 {
					return "", "", nil, nil, nil,
						fmt.Errorf("missing value after %s", tok)
				}
				continue
			}

			opts = append(opts, tok)
			continue
		}

		/* first non-option token is flags string */
		if flags == "" {
			flags = tok
		} else {
			return "", "", nil, nil, nil, errors.New("multiple flag strings found")
		}
	}

	if flags == "" {
		return "", "", nil, nil, nil, errors.New("missing metric flags string")
	}

	scope, err = parseScope(scopeArg)
	if err != nil {
		return "", "", nil, nil, nil, err
	}
	return scope, flags, opts, nsList, statuses, nil
}

/* ---------- pod phase (status) helpers ---------- */

// phaseNames maps any-case user input to the canonical PodPhase values
// expected by the Kubernetes API.
var phaseNames = map[string]string{
	"pending":   "Pending",
	"running":   "Running",
	"succeeded": "Succeeded",
	"failed":    "Failed",
	"unknown":   "Unknown",
}

// canonicalStatuses maps user-supplied status tokens (any case) to the
// canonical PodPhase values used in API field selectors.
func canonicalStatuses(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		canon, ok := phaseNames[strings.ToLower(s)]
		if !ok {
			return nil, fmt.Errorf("unknown status %q", s)
		}
		out = append(out, canon)
	}
	return out, nil
}

// defaultStatuses returns the -s filter to use: the user-supplied statuses,
// or Running when -s was not given.
func defaultStatuses(in []string) []string {
	if len(in) == 0 {
		return []string{"Running"}
	}
	return in
}

// phaseSelector builds a field selector matching a single canonical pod
// phase. Kubernetes field selectors cannot OR multiple values for the same
// field, so multiple phases are fetched with one selector per phase.
func phaseSelector(phase string) string {
	return "status.phase=" + phase
}

func usage(msg string) {
	if msg != "" {
		fmt.Fprintln(os.Stderr, "Error:", msg)
	}
	fmt.Fprint(os.Stderr, `Usage:
    kubectl ps <pods|nodes|namespaces> <flags> [options]

Scopes:
    pods | nodes | namespaces

Metric flags:
    m  memory      u  usage
    c  cpu         r  requests
    p  percent     l  limits
                   n  node  (pods only)
                   f  free  (nodes only)
                   t  total (nodes only)

Options:
    -A                all namespaces / all nodes
    -n <namespace>    select namespace (multiple space-separated allowed)
    -s <status>       filter pods by phase (default Running; multiple space-separated allowed)
    -r                reverse sort
    -h                human-readable units
    -m                mebibytes
    -g                gibibytes
    -b                bytes
    -t                show TOTAL
`)
	os.Exit(1)
}

func parseScope(s string) (string, error) {
	switch strings.ToLower(s) {
	case "pod", "pods", "po", "p":
		return "pods", nil
	case "node", "nodes", "no", "n":
		return "nodes", nil
	case "ns", "namespace", "namespaces":
		return "namespaces", nil
	default:
		return "", fmt.Errorf("unknown scope %s", s)
	}
}

func parseFlags(flags, scope string) columnCfg {
	var cfg columnCfg
	famSeen := map[rune]bool{}

	for _, ch := range flags {
		switch ch {
		case 'm', 'c':
			famSeen[ch] = true
		case 'n':
			if scope != "pods" {
				usage("flag n only valid for pods")
			}
			cfg.showNode = true
		default:
			if !isMetric(ch) {
				usage("unknown flag letter " + string(ch))
			}
			if isNodeOnly(ch) && scope != "nodes" {
				usage("flags f/t only valid for nodes scope")
			}
			cfg.metrics = append(cfg.metrics, ch)
		}
	}

	cfg.mem = famSeen['m']
	cfg.cpu = famSeen['c']
	if !cfg.mem && !cfg.cpu {
		usage("flags must include m and/or c")
	}
	if len(cfg.metrics) == 0 {
		usage("flags must include at least one metric letter (rlupft)")
	}
	return cfg
}

func containsRune(slice []rune, r rune) bool {
	for _, x := range slice {
		if x == r {
			return true
		}
	}
	return false
}

func filterRunes(slice []rune, keep func(rune) bool) []rune {
	out := make([]rune, 0, len(slice))
	for _, r := range slice {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func detectSort(flags string) (fam, metric rune) {
	fam, metric = 'm', 'r'
	for _, ch := range flags {
		if ch == 'm' || ch == 'c' {
			fam = ch
			break
		}
	}
	for _, ch := range flags {
		if isMetric(ch) {
			metric = ch
			break
		}
	}
	return
}

/* ---------- unit helpers ---------- */

type unitKind int

const (
	unitHuman unitKind = iota
	unitMi
	unitGi
	unitBytes
)

func memFmt(b int64, u unitKind) string {
	switch u {
	case unitBytes:
		return fmt.Sprintf("%d", b)
	case unitMi:
		return fmt.Sprintf("%.1f", float64(b)/1024/1024)
	case unitGi:
		return fmt.Sprintf("%.2f", float64(b)/1024/1024/1024)
	default:
		gb := float64(b) / 1024 / 1024 / 1024
		if gb >= 1 {
			return fmt.Sprintf("%.2fG", gb)
		}
		return fmt.Sprintf("%.1fM", float64(b)/1024/1024)
	}
}

func pct(second, first int64) string {
	if second <= 0 || first <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", float64(second)*100/float64(first))
}

func ageFmt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d.Hours() >= 48 {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d.Hours() >= 1 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

/* ---------- pods ---------- */

type podRow struct {
	ns, name, status, node string
	created                time.Time
	mem, cpu               map[rune]int64
}

func newMetricMap(metrics []rune) map[rune]int64 {
	m := make(map[rune]int64, len(metrics))
	for _, k := range metrics {
		m[k] = -1
	}
	return m
}

func runPods(cl *kubernetes.Clientset, mc *metricsclient.Clientset, curNS string, nsList []string, all bool, phases []string,
	cfg columnCfg, fam rune, metric rune, rev bool, u unitKind) {

	ctx := context.Background()
	usageMap := map[string]struct{ mem, cpu int64 }{}

	if containsRune(cfg.metrics, 'u') && mc != nil {
		if list, err := mc.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, pm := range list.Items {
				var mSum, cSum int64
				for _, c := range pm.Containers {
					mSum += c.Usage.Memory().Value()
					cSum += c.Usage.Cpu().MilliValue()
				}
				usageMap[key(pm.Namespace, pm.Name)] = struct{ mem, cpu int64 }{mSum, cSum}
			}
		}
	}

	pods, err := fetchPods(cl, nsList, curNS, all, phases)
	must(err)

	var rows []podRow
	for _, p := range pods.Items {
		r := podRow{
			ns:      p.Namespace,
			name:    p.Name,
			status:  string(p.Status.Phase),
			node:    p.Spec.NodeName,
			created: p.CreationTimestamp.Time,
			mem:     newMetricMap(cfg.metrics),
			cpu:     newMetricMap(cfg.metrics),
		}
		for _, c := range p.Spec.Containers {
			if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				r.mem['r'] = add64(r.mem['r'], q.Value())
			}
			if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				r.cpu['r'] = add64(r.cpu['r'], q.MilliValue())
			}
			if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
				r.mem['l'] = add64(r.mem['l'], q.Value())
			}
			if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
				r.cpu['l'] = add64(r.cpu['l'], q.MilliValue())
			}
		}
		if uDat, ok := usageMap[key(p.Namespace, p.Name)]; ok {
			r.mem['u'] = uDat.mem
			r.cpu['u'] = uDat.cpu
		}
		rows = append(rows, r)
	}

	sort.SliceStable(rows, func(i, j int) bool {
		less := podLess(rows[i], rows[j], fam, metric, cfg.metrics)
		if rev {
			return !less
		}
		return less
	})

	printPods(rows, cfg, all, fam, u)
}

// fetchPods lists pods matching any of the given canonical phases, across the
// selected namespaces: every namespace in nsList when -n was given, curNS
// otherwise, or all namespaces when all is set. Field selectors cannot OR
// multiple values for the same field, so one API call is made per phase.
func fetchPods(cl kubernetes.Interface, nsList []string, curNS string, all bool, phases []string) (*corev1.PodList, error) {
	if all {
		nsList = []string{""}
	} else if len(nsList) == 0 {
		nsList = []string{curNS}
	}

	var combined []corev1.Pod
	for _, ns := range nsList {
		for _, ph := range phases {
			pl, err := cl.CoreV1().Pods(ns).List(context.Background(),
				metav1.ListOptions{FieldSelector: phaseSelector(ph)})
			if err != nil {
				return nil, err
			}
			combined = append(combined, pl.Items...)
		}
	}
	return &corev1.PodList{Items: combined}, nil
}

func add64(a, b int64) int64 {
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	return a + b
}

func podLess(a, b podRow, fam, metric rune, metrics []rune) bool {
	val := func(r podRow) float64 {
		if metric == 'p' {
			if fam == 'c' {
				return percentValue(r.cpu, metrics)
			}
			return percentValue(r.mem, metrics)
		}
		if fam == 'c' {
			return float64(r.cpu[metric])
		}
		return float64(r.mem[metric])
	}
	return val(a) > val(b)
}

func printPods(rows []podRow, cfg columnCfg, all bool, fam rune, u unitKind) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if all {
		fmt.Fprint(tw, "NAMESPACE\t")
	}
	fmt.Fprint(tw, "NAME\tSTATUS\t")
	if cfg.showNode {
		fmt.Fprint(tw, "NODE\t")
	}
	writeHeaders(tw, cfg, fam)
	fmt.Fprint(tw, "AGE\n")

	totMem := newMetricMap(cfg.metrics)
	totCPU := newMetricMap(cfg.metrics)

	for _, r := range rows {
		if all {
			fmt.Fprintf(tw, "%s\t", r.ns)
		}
		fmt.Fprintf(tw, "%s\t%s\t", r.name, r.status)
		if cfg.showNode {
			fmt.Fprintf(tw, "%s\t", r.node)
		}
		writeRowMetrics(tw, r.mem, r.cpu, cfg, fam, u)
		fmt.Fprintf(tw, "%s\n", ageFmt(r.created))

		accumulateTotals(totMem, r.mem)
		accumulateTotals(totCPU, r.cpu)
	}

	if cfg.total {
		if all {
			fmt.Fprint(tw, "TOTAL\t-\t-\t")
		} else {
			fmt.Fprint(tw, "TOTAL\t-\t")
		}
		if cfg.showNode {
			fmt.Fprint(tw, "-\t")
		}
		writeRowMetrics(tw, totMem, totCPU, cfg, fam, u)
		fmt.Fprint(tw, "-\n")
	}

	tw.Flush()
}

/* ---------- helpers shared by all scopes ---------- */

func percentValue(mp map[rune]int64, metrics []rune) float64 {
	first, second := int64(-1), int64(-1)
	for _, m := range metrics {
		if m == 'p' || isNodeOnly(m) {
			continue
		}
		if first == -1 {
			first = mp[m]
		} else {
			second = mp[m]
			break
		}
	}
	if first <= 0 || second <= 0 {
		return -1
	}
	return float64(first) / float64(second)
}

func writeHeaders(tw *tabwriter.Writer, cfg columnCfg, fam rune) {
	short := map[rune]string{
		'r': "REQ", 'l': "LIM", 'u': "USE",
		'f': "FREE", 't': "TOTAL",
	}

	renderFam := func(f rune, enabled bool) {
		if !enabled {
			return
		}
		prefix := "MEM_"
		if f == 'c' {
			prefix = "CPU_"
		}

		numCols := []string{}
		for _, m := range cfg.metrics {
			if m != 'p' {
				numCols = append(numCols, short[m])
			}
		}

		printed := []string{}

		for _, m := range cfg.metrics {
			if m == 'p' {
				lbl := "PCT"
				if len(printed) >= 2 {
					lbl = printed[len(printed)-2] + "_" + printed[len(printed)-1]
				} else if len(numCols) >= 2 {
					lbl = numCols[0] + "_" + numCols[1]
				}
				fmt.Fprintf(tw, "%s%s\t", prefix, lbl)
				continue
			}
			fmt.Fprintf(tw, "%s%s\t", prefix, short[m])
			printed = append(printed, short[m])
		}
	}

	renderFam(fam, (fam == 'm' && cfg.mem) || (fam == 'c' && cfg.cpu))
	renderFam(otherFam(fam), (otherFam(fam) == 'm' && cfg.mem) || (otherFam(fam) == 'c' && cfg.cpu))
}

func writeRowMetrics(tw *tabwriter.Writer, mem, cpu map[rune]int64,
	cfg columnCfg, fam rune, u unitKind) {

	render := func(f rune, mp map[rune]int64, enabled bool) {
		if !enabled {
			return
		}

		var printed []int64

		firstTwo := func() (int64, int64) {
			var a, b int64 = -1, -1
			for _, m := range cfg.metrics {
				if m == 'p' {
					continue
				}
				if a == -1 {
					a = mp[m]
				} else {
					b = mp[m]
					break
				}
			}
			return a, b
		}

		for _, m := range cfg.metrics {
			if m == 'p' {
				var x, y int64
				if len(printed) >= 2 {
					x, y = printed[len(printed)-2], printed[len(printed)-1]
				} else {
					x, y = firstTwo()
				}
				if x > 0 && y > 0 {
					fmt.Fprintf(tw, "%s\t", pct(x, y))
				} else {
					fmt.Fprint(tw, "-\t")
				}
				continue
			}

			val := mp[m]
			if f == 'm' {
				if val >= 0 {
					fmt.Fprintf(tw, "%s\t", memFmt(val, u))
				} else {
					fmt.Fprint(tw, "-\t")
				}
			} else {
				if val >= 0 {
					fmt.Fprintf(tw, "%d\t", val)
				} else {
					fmt.Fprint(tw, "-\t")
				}
			}
			printed = append(printed, val)
		}
	}

	render(fam, mapSelect(fam, mem, cpu), (fam == 'm' && cfg.mem) || (fam == 'c' && cfg.cpu))
	render(otherFam(fam), mapSelect(otherFam(fam), mem, cpu), (otherFam(fam) == 'm' && cfg.mem) || (otherFam(fam) == 'c' && cfg.cpu))
}

func mapSelect(f rune, mem, cpu map[rune]int64) map[rune]int64 {
	if f == 'm' {
		return mem
	}
	return cpu
}

func accumulateTotals(tot, add map[rune]int64) {
	for k, v := range add {
		if v < 0 {
			continue
		}
		if tot[k] < 0 {
			tot[k] = 0
		}
		tot[k] += v
	}
}

/* ---------- nodes ---------- */

type nodeRow struct {
	name, status string
	created      time.Time
	mem, cpu     map[rune]int64
}

func runNodes(cl *kubernetes.Clientset, mc *metricsclient.Clientset, cfg columnCfg, fam rune,
	metric rune, rev bool, u unitKind) {

	ctx := context.Background()
	nodes, err := cl.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	must(err)

	idx := map[string]*nodeRow{}
	var rows []nodeRow

	for _, n := range nodes.Items {
		st := "NotReady"
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				st = "Ready"
				break
			}
		}
		r := nodeRow{
			name:    n.Name,
			status:  st,
			created: n.CreationTimestamp.Time,
			mem:     newMetricMap(cfg.metrics),
			cpu:     newMetricMap(cfg.metrics),
		}
		r.mem['l'] = n.Status.Allocatable.Memory().Value()
		r.cpu['l'] = n.Status.Allocatable.Cpu().MilliValue()
		rows = append(rows, r)
		idx[n.Name] = &rows[len(rows)-1]
	}

	podNode := map[string]string{}
	if pods, _ := cl.CoreV1().Pods("").List(ctx, metav1.ListOptions{}); pods != nil {
		for _, p := range pods.Items {
			nr := idx[p.Spec.NodeName]
			if nr == nil {
				continue
			}
			podNode[key(p.Namespace, p.Name)] = p.Spec.NodeName
			for _, c := range p.Spec.Containers {
				if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
					nr.mem['r'] = add64(nr.mem['r'], q.Value())
				}
				if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
					nr.cpu['r'] = add64(nr.cpu['r'], q.MilliValue())
				}
			}
		}
	}

	if (containsRune(cfg.metrics, 'u') || containsRune(cfg.metrics, 'f')) && mc != nil {
		if list, err := mc.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, pm := range list.Items {
				node := podNode[key(pm.Namespace, pm.Name)]
				nr := idx[node]
				if nr == nil {
					continue
				}
				for _, c := range pm.Containers {
					nr.mem['u'] = add64(nr.mem['u'], c.Usage.Memory().Value())
					nr.cpu['u'] = add64(nr.cpu['u'], c.Usage.Cpu().MilliValue())
				}
			}
		}
	}

	for _, nr := range rows {
		if containsRune(cfg.metrics, 'f') {
			if nr.mem['l'] >= 0 && nr.mem['u'] >= 0 {
				nr.mem['f'] = nr.mem['l'] - nr.mem['u']
			}
			if nr.cpu['l'] >= 0 && nr.cpu['u'] >= 0 {
				nr.cpu['f'] = nr.cpu['l'] - nr.cpu['u']
			}
		}
		if containsRune(cfg.metrics, 't') {
			nr.mem['t'] = nr.mem['l']
			nr.cpu['t'] = nr.cpu['l']
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		less := nodeLess(rows[i], rows[j], fam, metric, cfg.metrics)
		if rev {
			return !less
		}
		return less
	})

	printNodes(rows, cfg, fam, u)
}

func nodeLess(a, b nodeRow, fam, metric rune, metrics []rune) bool {
	val := func(r nodeRow) float64 {
		if metric == 'p' {
			if fam == 'c' {
				return percentValue(r.cpu, metrics)
			}
			return percentValue(r.mem, metrics)
		}
		if fam == 'c' {
			return float64(r.cpu[metric])
		}
		return float64(r.mem[metric])
	}
	return val(a) > val(b)
}

func printNodes(rows []nodeRow, cfg columnCfg, fam rune, u unitKind) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprint(tw, "NAME\tSTATUS\t")
	writeHeaders(tw, cfg, fam)
	fmt.Fprint(tw, "AGE\n")

	totMem := newMetricMap(cfg.metrics)
	totCPU := newMetricMap(cfg.metrics)

	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t", r.name, r.status)
		writeRowMetrics(tw, r.mem, r.cpu, cfg, fam, u)
		fmt.Fprintf(tw, "%s\n", ageFmt(r.created))

		accumulateTotals(totMem, r.mem)
		accumulateTotals(totCPU, r.cpu)
	}

	if cfg.total {
		fmt.Fprint(tw, "TOTAL\t-\t")
		writeRowMetrics(tw, totMem, totCPU, cfg, fam, u)
		fmt.Fprint(tw, "-\n")
	}

	tw.Flush()
}

/* ---------- namespaces ---------- */

type nsRow struct {
	name, status string
	created      time.Time
	mem, cpu     map[rune]int64
}

func runNamespaces(cl *kubernetes.Clientset, mc *metricsclient.Clientset, cfg columnCfg,
	fam rune, metric rune, rev bool, u unitKind) {

	ctx := context.Background()
	list, err := cl.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	must(err)

	idx := map[string]*nsRow{}
	var rows []nsRow

	for _, n := range list.Items {
		r := nsRow{
			name:    n.Name,
			status:  string(n.Status.Phase),
			created: n.CreationTimestamp.Time,
			mem:     newMetricMap(cfg.metrics),
			cpu:     newMetricMap(cfg.metrics),
		}
		rows = append(rows, r)
		idx[n.Name] = &rows[len(rows)-1]
	}

	if pods, _ := cl.CoreV1().Pods("").List(ctx, metav1.ListOptions{}); pods != nil {
		for _, p := range pods.Items {
			nr := idx[p.Namespace]
			if nr == nil {
				continue
			}
			for _, c := range p.Spec.Containers {
				if q, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
					nr.mem['r'] = add64(nr.mem['r'], q.Value())
				}
				if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
					nr.cpu['r'] = add64(nr.cpu['r'], q.MilliValue())
				}
				if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
					nr.mem['l'] = add64(nr.mem['l'], q.Value())
				}
				if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
					nr.cpu['l'] = add64(nr.cpu['l'], q.MilliValue())
				}
			}
		}
	}

	if containsRune(cfg.metrics, 'u') && mc != nil {
		if lst, err := mc.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{}); err == nil {
			for _, pm := range lst.Items {
				nr := idx[pm.Namespace]
				if nr == nil {
					continue
				}
				for _, c := range pm.Containers {
					nr.mem['u'] = add64(nr.mem['u'], c.Usage.Memory().Value())
					nr.cpu['u'] = add64(nr.cpu['u'], c.Usage.Cpu().MilliValue())
				}
			}
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		less := nsLess(rows[i], rows[j], fam, metric, cfg.metrics)
		if rev {
			return !less
		}
		return less
	})

	printNS(rows, cfg, fam, u)
}

func nsLess(a, b nsRow, fam, metric rune, metrics []rune) bool {
	val := func(r nsRow) float64 {
		if metric == 'p' {
			if fam == 'c' {
				return percentValue(r.cpu, metrics)
			}
			return percentValue(r.mem, metrics)
		}
		if fam == 'c' {
			return float64(r.cpu[metric])
		}
		return float64(r.mem[metric])
	}
	return val(a) > val(b)
}

func printNS(rows []nsRow, cfg columnCfg, fam rune, u unitKind) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	fmt.Fprint(tw, "NAME\tSTATUS\t")
	writeHeaders(tw, cfg, fam)
	fmt.Fprint(tw, "AGE\n")

	totMem := newMetricMap(cfg.metrics)
	totCPU := newMetricMap(cfg.metrics)

	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t", r.name, r.status)
		writeRowMetrics(tw, r.mem, r.cpu, cfg, fam, u)
		fmt.Fprintf(tw, "%s\n", ageFmt(r.created))

		accumulateTotals(totMem, r.mem)
		accumulateTotals(totCPU, r.cpu)
	}

	if cfg.total {
		fmt.Fprint(tw, "TOTAL\t-\t")
		writeRowMetrics(tw, totMem, totCPU, cfg, fam, u)
		fmt.Fprint(tw, "-\n")
	}

	tw.Flush()
}

/* ---------- misc helpers ---------- */

func otherFam(f rune) rune {
	if f == 'm' {
		return 'c'
	}
	return 'm'
}

func key(ns, name string) string { return ns + "/" + name }

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mustClient(cfg *rest.Config) *kubernetes.Clientset {
	c, err := kubernetes.NewForConfig(cfg)
	must(err)
	return c
}

func mustBuildConfig() (*rest.Config, string) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfgLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	ns, _, err := cfgLoader.Namespace()
	must(err)
	restCfg, err := cfgLoader.ClientConfig()
	must(err)
	if ns == "" {
		ns = "default"
	}
	return restCfg, ns
}
