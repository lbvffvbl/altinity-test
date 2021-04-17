package main

import (
	"context"
	"github.com/docker/go-units"
	"github.com/kelseyhightower/envconfig"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"net/http"
	"os"
	"time"
)

type PvInfo struct {
	size  int64
	count int
}

type NodesAttr struct {
	Zone string
	Type string
}

type PvAttr struct {
	Zone  string
	Class string
}

type envConfig struct {
	// Current UnitSize
	UnitSize string `envconfig:"UNITSIZE" required:"false"`
}

var (
	nodesGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "node_count",
			Help: "Nodes counts grouped by instance types and availability Zones",
		},
		[]string{
			"instanceType",
			"availabilityZone"},
	)
	pvcGaugeSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pvc_size",
			Help: "PVC size grouped by storage className and availability Zones",
		},
		[]string{
			"storageClassName",
			"availabilityZone"},
	)
	pvGaugeSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pv_capacity",
			Help: "PV capacity grouped by storage className and availability Zones",
		},
		[]string{
			"storageClassName",
			"availabilityZone",
		},
	)
	pvGaugeCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pv_count",
			Help: "PV count grouped by className and availability Zones",
		},
		[]string{
			"storageClassName",
			"availabilityZone"},
	)
	pvcGaugeCount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "pvc_count",
			Help: "PVC count grouped by className and availability Zones ",
		},
		[]string{
			"storageClassName",
			"availabilityZone"},
	)
	ErrorsGauageVec = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "altinity_test_errors",
			Help: "Metrics of not fatal errors requiring attention",
		},
		[]string{
			"type",
		},
	)
	WarningLogger *log.Logger
	InfoLogger    *log.Logger
	ErrorLogger   *log.Logger
	Env           envConfig
	WarningState  bool
)

func groupNodes(nodeList []v1.Node) map[NodesAttr]int {
	nodesSum := map[NodesAttr]int{}
	for _, node := range nodeList {
		instanceType := "Unknown"
		zone := "Unknown"
		if _, ok := node.Labels["node.kubernetes.io/instance-type"]; ok {
			instanceType = node.Labels["node.kubernetes.io/instance-type"]
		}
		if _, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
			zone = node.Labels["topology.kubernetes.io/zone"]
		}

		typeZone := NodesAttr{
			Type: instanceType,
			Zone: zone,
		}
		nodesSum[typeZone] += 1
		typeOnly := NodesAttr{
			Type: instanceType,
		}
		nodesSum[typeOnly] += 1
		zoneOnly := NodesAttr{
			Zone: zone,
		}
		nodesSum[zoneOnly] += 1

	}
	return nodesSum
}

func groupPV(pvList []v1.PersistentVolume, pvcList []v1.PersistentVolumeClaim) (map[PvAttr]PvInfo, map[PvAttr]PvInfo, int64, int64) {
	var pvSizeTotal int64
	var pvcSizeTotal int64
	pvGrouped := map[PvAttr]PvInfo{}
	pvParsed := map[string]PvAttr{} // for matching zones in PVC
	for _, pv := range pvList {
		name := pv.Name
		size := pv.Spec.Capacity.Storage().Value()
		className := pv.Spec.StorageClassName
		zone := "Unknown"
		if pv.Spec.NodeAffinity != nil {
			if pv.Spec.NodeAffinity.Required.NodeSelectorTerms != nil {
				for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
					if term.MatchExpressions != nil {
						for _, match := range term.MatchExpressions {
							if match.Key == "failure-domain.beta.kubernetes.io/zone" || //deprecated label
								match.Key == "topology.kubernetes.io/zone" ||
								match.Key == "topology.ebs.csi.aws.com/zone" { //used for ebs
								if len(match.Values) > 1 {
									WarningLogger.Printf("For PV %s more then one availability zones %s, will be used first one %s", name, match.Values, match.Values[0])
									WarningState = true
								}
								zone = match.Values[0]
							}
						}
					}
				}
			}
		}
		zoneClass := PvAttr{
			Zone:  zone,
			Class: className,
		}
		diff := pvGrouped[zoneClass]
		diff.size += size
		diff.count += 1
		pvGrouped[zoneClass] = diff

		classOnly := PvAttr{
			Class: className,
		}
		diffClass := pvGrouped[classOnly]
		diffClass.size += size
		diffClass.count += 1
		pvGrouped[classOnly] = diffClass

		zoneOnly := PvAttr{
			Zone: zone,
		}
		diffZone := pvGrouped[zoneOnly]
		diffZone.size += size
		diffZone.count += 1
		pvGrouped[zoneOnly] = diffZone

		pvParsed[name] = PvAttr{
			Zone:  zone,
			Class: className}

		pvSizeTotal += size

	}
	pvcGrouped := map[PvAttr]PvInfo{}
	for _, pvc := range pvcList {
		size := pvc.Spec.Resources.Requests.Storage().Value()
		className := *pvc.Spec.StorageClassName
		zone := "Unknown"
		if pvc.Spec.VolumeName != "" {
			zone = pvParsed[pvc.Spec.VolumeName].Zone
		}
		zoneClass := PvAttr{
			Zone:  zone,
			Class: className,
		}
		diff := pvcGrouped[zoneClass]
		diff.size += size
		diff.count += 1
		pvcGrouped[zoneClass] = diff

		classOnly := PvAttr{
			Class: className,
		}
		diffClass := pvcGrouped[classOnly]
		diffClass.size += size
		diffClass.count += 1
		pvcGrouped[classOnly] = diffClass

		zoneOnly := PvAttr{
			Zone: zone,
		}
		diffZone := pvcGrouped[zoneOnly]
		diffZone.size += size
		diffZone.count += 1
		pvcGrouped[zoneOnly] = diffZone

		pvcSizeTotal += size
	}
	return pvGrouped, pvcGrouped, pvSizeTotal, pvcSizeTotal
}

func setValues(pvGrouped, pvcGrouped map[PvAttr]PvInfo, nodesGrouped map[NodesAttr]int, nodeCountTotal, pvCountTotal, pvcCountTotal int, pvSizeTotal, pvcSizeTotal int64) {
	unitSize := getUnitSize(Env.UnitSize)
	nodesGauge.Reset()
	pvcGaugeSize.Reset()
	pvGaugeSize.Reset()
	pvcGaugeCount.Reset()
	pvGaugeCount.Reset()
	nodesGauge.WithLabelValues("all", "all").Set(float64(nodeCountTotal))
	pvcGaugeCount.WithLabelValues("all", "all").Set(float64(pvcCountTotal))
	pvGaugeCount.WithLabelValues("all", "all").Set(float64(pvCountTotal))
	pvcGaugeSize.WithLabelValues("all", "all").Set(float64(pvcSizeTotal) / unitSize)
	pvGaugeSize.WithLabelValues("all", "all").Set(float64(pvSizeTotal) / unitSize)
	if WarningState {
		ErrorsGauageVec.WithLabelValues("ExtraZones").Set(1)
	} else {
		ErrorsGauageVec.WithLabelValues("ExtraZones").Set(0)
	}
	for keys, value := range nodesGrouped {
		if keys.Type == "" {
			keys.Type = "all"
		}
		if keys.Zone == "" {
			keys.Zone = "all"
		}
		nodesGauge.WithLabelValues(keys.Type, keys.Zone).Set(float64(value))
	}
	for keys, value := range pvGrouped {
		if keys.Class == "" {
			keys.Class = "all"
		}
		if keys.Zone == "" {
			keys.Zone = "all"
		}
		pvGaugeSize.WithLabelValues(keys.Class, keys.Zone).Set(float64(value.size) / unitSize)
		pvGaugeCount.WithLabelValues(keys.Class, keys.Zone).Set(float64(value.count))
	}
	for keys, value := range pvcGrouped {
		if keys.Class == "" {
			keys.Class = "all"
		}
		if keys.Zone == "" {
			keys.Zone = "all"
		}
		pvcGaugeSize.WithLabelValues(keys.Class, keys.Zone).Set(float64(value.size) / unitSize)
		pvcGaugeCount.WithLabelValues(keys.Class, keys.Zone).Set(float64(value.count))
	}

}

func collectMetrics() {

	InfoLogger.Printf("Start collect metrics")
	config, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		panic(err.Error())
	}
	clientSet, err := kubernetes.NewForConfig(config)
	api := clientSet.CoreV1()
	if err != nil {
		panic(err.Error())
	}
	for {
		WarningState = false
		nodes, err := api.Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}
		nodesGrouped := groupNodes(nodes.Items)

		pv, err := api.PersistentVolumes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		pvc, err := api.PersistentVolumeClaims("").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}
		pvGrouped, pvcGrouped, pvSizeTotal, pvcSizeTotal := groupPV(pv.Items, pvc.Items)
		nodeCountTotal := len(nodes.Items)
		pvCountTotal := len(pv.Items)
		pvcCountTotal := len(pvc.Items)
		setValues(pvGrouped, pvcGrouped, nodesGrouped, nodeCountTotal, pvCountTotal, pvcCountTotal, pvSizeTotal, pvcSizeTotal)
		time.Sleep(1 * time.Minute)

	}
}

func getUnitSize(VarSize string) float64 {
	unitSizeValue := 1
	switch VarSize {
	case "KiB":
		unitSizeValue = units.KiB
	case "MiB":
		unitSizeValue = units.MiB
	case "GiB":
		unitSizeValue = units.GiB
	case "TiB":
		unitSizeValue = units.TiB
	case "PiB":
		unitSizeValue = units.PiB
	}
	if unitSizeValue != 1 {
		InfoLogger.Printf("Got Unitsize value. Size metricw will be present in %s", VarSize)
	}
	return float64(unitSizeValue)

}

func init() {
	InfoLogger = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	WarningLogger = log.New(os.Stdout, "WARNING: ", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLogger = log.New(os.Stdout, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

func main() {
	if err := envconfig.Process("", &Env); err != nil {
		ErrorLogger.Printf("Failed to process env var: %s", err)
		panic(err.Error())
	}
	InfoLogger.Printf("ENV UnitSize: \"%s\"", Env.UnitSize)
	err := prometheus.Register(nodesGauge)
	if err != nil {
		panic(err.Error())
	}
	err = prometheus.Register(pvcGaugeSize)
	if err != nil {
		panic(err.Error())
	}
	err = prometheus.Register(pvGaugeSize)
	if err != nil {
		panic(err.Error())
	}
	err = prometheus.Register(pvcGaugeCount)
	if err != nil {
		panic(err.Error())
	}
	err = prometheus.Register(pvGaugeCount)
	if err != nil {
		panic(err.Error())
	}
	err = prometheus.Register(ErrorsGauageVec)
	if err != nil {
		panic(err.Error())
	}

	go collectMetrics()

	http.Handle("/metrics", promhttp.Handler())
	err = http.ListenAndServe(":3000", nil)
	if err != nil {
		panic(err.Error())
	}
}
