package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
	"github.com/op/go-logging"
	"net/http"
	"net/url"
	"time"
	_ "github.com/go-sql-driver/mysql"
	"database/sql"
	"os"
	"strings"
	"crypto/tls"
	"strconv"
)

type Pusher struct {
	health prometheus.Counter
}

var logFile, _ = os.OpenFile("/var/log/cnc_detect.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)

func query_address(db sql.DB, log *logging.Logger) (map[string]string, error) {
	var host string
	var pn_title string
	var proxy_map map[string]string
	proxy_map = make(map[string]string)
	QueryString := "SELECT host,pn_title FROM cdn_node_stat WHERE pn_title like 'cnc%'"
	rows, err := db.Query(QueryString)
	if err != nil {
		log.Error("query failed! reason: ", err)
		return nil, err
	}
	for rows.Next() {
		err := rows.Scan(&host, &pn_title)
		if err != nil {
			log.Error(err)
			return nil, err
		}
		proxy_map[pn_title] = host
	}
	err = rows.Err()
	if err != nil {
		log.Error(err)
	}
	defer rows.Close()
	defer db.Close()
	return proxy_map, nil
}

func get_sn(db sql.DB, log *logging.Logger) (string, error){
	var sn string
	QueryString := "SELECT sn FROM cache_service_stat limit 1"
	rows, err := db.Query(QueryString)
	if err != nil {
		log.Error("get sn failed! reason: ", err)
		return "", err
	}
	for rows.Next() {
		err := rows.Scan(&sn)
		if err != nil {
			log.Error("get sn failed! reason: ", err)
			return "", err
		}
	}
	err = rows.Err()
	if err != nil {
		log.Error(err)
		return "", err
	}
	return sn, nil
}

func cnc_get(baseurl string, proxyurl string) (map[string]float64, error){
	var result map[string]float64
	result = make(map[string]float64)
	proxy, _ := url.Parse("http://" + proxyurl)
	tr := &http.Transport{
		Proxy: http.ProxyURL(proxy),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{
		Transport: tr,
		Timeout: time.Second * 5,
	}
	start := time.Now()
	resp, err := client.Get(baseurl)
	elapsed := time.Since(start)
	if err != nil {
		return nil, err
	}
	code := resp.StatusCode
	result["return_code"] = float64(code)
	result["elapsed"] = float64(elapsed/time.Millisecond)
	return result, nil
}

func duration_push(pntitle string, urltype string, code float64, elapsed float64, sn string, success string, log *logging.Logger) {
	cnc_elapsed := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "cnc_http_detect",
		Help: "The elapsed of " + pntitle + ".",
		ConstLabels: prometheus.Labels{"success": success, "status": strconv.Itoa(int(code))},
		Buckets: prometheus.ExponentialBuckets(500, 1.6, 5),

	})
	cnc_elapsed.Observe(elapsed)
	if err := push.New("http://oss.fxdata.cn:9091", pntitle + "_elapsed_detect_" + urltype).
		Collector(cnc_elapsed).Grouping("instance", sn).
		Add(); err != nil {
		log.Error("Could not push completion time to Pushgateway:", err)
	}
}


func (p *Pusher)health_push(sn string, log *logging.Logger) {
	p.health.Inc()
	if err := push.New("http://oss.fxdata.cn:9091", "cnc_health").
		Collector(p.health).Grouping("instance", sn).
		Add(); err != nil {
		log.Error("Could not push completion time to Pushgateway:", err)
	}
}

func cnc_push(baseurl string, proxyurl string, proxynode string, log *logging.Logger, sn string){
	if baseurl == "http://lvs.lxdns.net/wstest/100k.jpg"{
		res, err := cnc_get(baseurl, proxyurl)
		if err != nil{
			log.Error("get response from " + proxynode + " failed!: ", err)
			duration_push(proxynode,"100k", 0, 0, sn, "False", log)
		}
		duration_push(proxynode, "100k", res["return_code"], res["elapsed"], sn, "True", log)
	} else {
		res, err := cnc_get(baseurl, proxyurl)
		if err != nil{
			log.Error("get response from " + proxynode + " failed!: ", err)
			duration_push(proxynode, "8B", 0, 0, sn, "False", log)
		}
		duration_push(proxynode, "8B", res["return_code"], res["elapsed"], sn, "True", log)
	}
}

func main() {
	var log = logging.MustGetLogger("example")

	var format = logging.MustStringFormatter(
		`%{color}%{time:2006-01-02 15:04:05.000} %{shortfunc} > %{level:.4s} %{id:03x}%{color:reset} %{message}`,
	)
	backend1 := logging.NewLogBackend(logFile, "", 0)
	backend2 := logging.NewLogBackend(os.Stderr, "", 0)

	backend2Formatter := logging.NewBackendFormatter(backend2, format)
	backend1Formatter := logging.NewBackendFormatter(backend1, format)
	logging.SetBackend(backend1Formatter, backend2Formatter)

	db, _ := sql.Open("mysql", "root@unix(/var/lib/mysql/mysql.sock)/cache?charset=utf8")
	proxy_map, err := query_address(*db, log)
	if err != nil{
		log.Error("get proxy map failed!")
		panic(err)
	}
	sn , err := get_sn(*db, log)
	if err != nil{
		log.Error("get sn failed!")
		panic(err)
	}

	p := &Pusher{health: prometheus.NewCounter(prometheus.CounterOpts{Name: "cnc_health_detect", Help: "The health check of cnc node.",})}


	for {
		for pn_title, host := range proxy_map {
			if strings.Contains(pn_title, "http") {
				cnc_push("http://lvs.lxdns.net/testcdn.htm", host, pn_title, log, sn)
			} else {
				cnc_push("http://lvs.lxdns.net/wstest/100k.jpg", host, pn_title, log, sn)
				cnc_push("http://lvs.lxdns.net/testcdn.htm", host, pn_title, log, sn)
			}
			p.health_push(sn, log)
		}
		time.Sleep(time.Second * 60)
	}
}
