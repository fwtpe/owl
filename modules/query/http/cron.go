package http

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/astaxie/beego/orm"
	"github.com/jasonlvhit/gocron"
	"github.com/jmoiron/sqlx"

	"github.com/fwtpe/owl-backend/common/db"
	osqlx "github.com/fwtpe/owl-backend/common/db/sqlx"
	cmodel "github.com/fwtpe/owl-backend/common/model"
	"github.com/fwtpe/owl-backend/common/utils"

	qdb "github.com/fwtpe/owl-backend/modules/query/database"
	"github.com/fwtpe/owl-backend/modules/query/g"
	"github.com/fwtpe/owl-backend/modules/query/graph"
	"github.com/fwtpe/owl-backend/modules/query/http/boss"
	bmodel "github.com/fwtpe/owl-backend/modules/query/model/boss"
)

type IDCMapItem struct {
	Popid    int    `db:"popid"`
	Idc      string `db:"idc"`
	Province string `db:"province"`
	City     string `db:"city"`
}

type Contacts struct {
	Id      int
	Name    string
	Phone   string
	Email   string
	Updated string
}

type Hosts struct {
	Id        int
	Hostname  string
	Exist     int
	Activate  int
	Platform  string
	Platforms string
	Idc       string
	Ip        string
	Isp       string
	Province  string
	City      string
	Status    string
	Bonding   int
	Speed     int
	Remark    string
	Updated   string
}

type Idcs struct {
	Id        int
	Popid     int
	Idc       string
	Bandwidth int
	Count     int
	Area      string
	Province  string
	City      string
	Updated   string
}

type Ips struct {
	Id       int
	Ip       string
	Exist    int
	Status   int
	Type     string
	Hostname string
	Platform string
	Updated  string
}

type Platforms struct {
	Id          int
	Platform    string
	Type        string
	Visible     int
	Contacts    string
	Principal   string
	Deputy      string
	Upgrader    string
	Count       int
	Department  string
	Team        string
	Description string
	Updated     string
}

func SyncHostsAndContactsTable() {
	if g.Config().Hosts.Enabled || g.Config().Contacts.Enabled {
		if g.Config().Hosts.Enabled {
			syncIdcData()
			syncHostData()
			intervalToSyncHostsTable := uint64(g.Config().Hosts.Interval)
			gocron.Every(intervalToSyncHostsTable).Seconds().Do(syncHostData)
			intervalToSyncContactsTable := uint64(g.Config().Contacts.Interval)
			gocron.Every(intervalToSyncContactsTable).Seconds().Do(syncIdcData)
		}
		if g.Config().Contacts.Enabled {
			syncContactsTable()
			intervalToSyncContactsTable := uint64(g.Config().Contacts.Interval)
			gocron.Every(intervalToSyncContactsTable).Seconds().Do(syncContactsTable)
		}
		if g.Config().Net.Enabled {
			syncNetTable()
			gocron.Every(1).Day().At(g.Config().Net.Time).Do(syncNetTable)
		}
		if g.Config().Deviations.Enabled {
			syncDeviationsTable()
			gocron.Every(1).Day().At(g.Config().Deviations.Time).Do(syncDeviationsTable)
		}
		if g.Config().Speed.Enabled {
			addBondingAndSpeedToHostsTable()
			gocron.Every(1).Day().At(g.Config().Speed.Time).Do(addBondingAndSpeedToHostsTable)
		}
		<-gocron.Start()
	}
}

func syncIdcData() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[syncIdcData()] Got panic: %v", r)
		}
	}()

	// Checks whether or not the latested update time of table is passed than <N> seconds
	now := time.Now()
	intervalSeconds := g.Config().Contacts.Interval
	log.Debugf("[Refresh \"idcs\"] Current time: [%s]. Interval: %d seconds", now, intervalSeconds)
	if !isElapsedTimePassedForIdcsTable(now, intervalSeconds) {
		log.Debugf("Skip synchronization")
		return
	}

	idcData := make(map[string]*sourceIdcRow)
	for _, row := range boss.LoadIdcData() {
		for _, host := range row.IpList {
			if _, ok := idcData[host.Pop]; ok {
				continue
			}

			log.Debugf("Process IDC [%s(%s)]. IP: [%s]", host.PopId, host.Pop, host.Ip)

			idcId, err := strconv.Atoi(host.PopId)
			if err != nil {
				log.Errorf("Cannot convert popId[%s] to integer", host.PopId)
				continue
			}
			location := getLocation(idcId)

			bandwidthData := make(map[string]interface{})
			queryIDCsBandwidths(host.Pop, bandwidthData)
			bandwidthData = bandwidthData["items"].(map[string]interface{})

			idcData[host.Pop] = &sourceIdcRow{
				id: int32(idcId), name: host.Pop,
				location: &bmodel.Location{
					Area:     location["area"],
					Province: location["province"],
					City:     location["city"],
				},
				bandwidth: int(bandwidthData["upperLimitMB"].(float64)),
			}
		}
	}

	updateIdcData(idcData)
}

type sourceIdcRow struct {
	id        int32
	name      string
	location  *bmodel.Location
	bandwidth int
}

func getHostsBondingAndSpeed(hostname string) map[string]int {
	item := map[string]int{}
	param := cmodel.GraphLastParam{
		Endpoint: hostname,
	}
	param.Counter = "nic.bond.mode"
	resp, err := graph.Last(param)
	if err != nil {
		log.Errorf(err.Error())
	} else if resp != nil {
		value := int(resp.Value.Value)
		if value >= 0 {
			item["bonding"] = value
		}
	}
	param.Counter = "nic.default.out.speed"
	resp, err = graph.Last(param)
	if err != nil {
		log.Errorf(err.Error())
	} else if resp != nil {
		value := int(resp.Value.Value)
		if value > 0 {
			item["speed"] = value
		}
	}
	return item
}

func addBondingAndSpeedToHostsTable() {
	log.Debugf("func addBondingAndSpeedToHostsTable()")
	o := NewBossOrm()
	var rows []orm.Params
	sql := "SELECT id, hostname FROM `boss`.`hosts` WHERE exist = 1"
	num, err := o.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
	} else if num > 0 {
		var host Hosts
		for _, row := range rows {
			hostname := row["hostname"].(string)
			item := getHostsBondingAndSpeed(hostname)
			err = o.QueryTable("hosts").Filter("hostname", hostname).One(&host)
			if err != nil {
				log.Errorf(err.Error())
			} else {
				if _, ok := item["bonding"]; ok {
					host.Bonding = item["bonding"]
				}
				if _, ok := item["speed"]; ok {
					host.Speed = item["speed"]
				}
				host.Updated = getNow()
				_, err = o.Update(&host)
				if err != nil {
					log.Errorf(err.Error())
				}
			}
		}
	}
}

func loadDetailOfMatchedPlatforms(neededPlatforms map[string]bool) []*bmodel.PlatformDetail {
	targetPlatforms := make([]*bmodel.PlatformDetail, 0)
	for _, platform := range boss.LoadDetailOfPlatforms() {
		if _, ok := neededPlatforms[platform.Name]; ok {
			targetPlatforms = append(targetPlatforms, platform)
		}
	}

	return targetPlatforms
}

func getDurationForNetTableQuery(offset int) (int64, int64) {
	year, month, day := time.Now().Date()
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		loc = time.Local
	}
	timestampFrom := time.Date(year, month, day-offset, 0, 0, 0, 0, loc).Unix() - 300
	timestampTo := time.Date(year, month, day-offset, 23, 59, 59, 0, loc).Unix()
	return timestampFrom, timestampTo
}

func getPlatformsDailyTrafficData(platformName string, offset int) (map[string]map[string]int, string, map[string]int) {
	data := map[string]map[string]int{
		"in":  {},
		"out": {},
	}
	date := ""
	counts := map[string]int{
		"in":  0,
		"out": 0,
	}
	hostnames := []string{}
	var rows []orm.Params
	o := NewBossOrm()
	sql := "SELECT DISTINCT hostname FROM `boss`.`ips`"
	sql += " WHERE platform = ? AND exist = 1 ORDER BY hostname ASC"
	num, err := o.Raw(sql, platformName).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
	} else if num > 0 {
		for _, row := range rows {
			hostnames = append(hostnames, row["hostname"].(string))
		}
	}
	metrics := getMetricsByMetricType("bandwidths")
	timestampFrom, timestampTo := getDurationForNetTableQuery(offset)
	responses := []*cmodel.GraphQueryResponse{}
	for _, hostname := range hostnames {
		for _, metric := range metrics {
			request := cmodel.GraphQueryParam{
				Endpoint:  hostname,
				Counter:   metric,
				Start:     timestampFrom,
				End:       timestampTo,
				ConsolFun: "AVERAGE",
				Step:      1200,
			}
			response, err := graph.QueryOne(request)
			if err != nil {
				log.Debugf("graph.queryOne fail = %v", err.Error())
			} else {
				responses = append(responses, response)
			}
		}
	}
	dataRaw := map[string]map[string]float64{
		"in":  {},
		"out": {},
	}
	tickers := []string{}
	if len(responses) > 0 {
		index := -1
		max := 0
		for key, item := range responses {
			if max < len(item.Values) {
				max = len(item.Values)
				index = key
			}
		}
		if index == -1 {
			date = time.Unix(timestampTo, 0).Format("2006-01-02")
			return data, date, counts
		}
		unit := 20
		tickersMap := map[string]float64{}
		for _, rrdObj := range responses[index].Values {
			ticker := getTicker(rrdObj.Timestamp, unit)
			if _, ok := tickersMap[ticker]; !ok {
				if len(ticker) > 0 {
					tickersMap[ticker] = float64(0)
					tickers = append(tickers, ticker)
				}
			}
		}
		for _, series := range responses {
			metric := strings.Replace(series.Counter, "net.if.", "", -1)
			metric = strings.Replace(metric, ".bits/iface=eth_all", "", -1)
			for _, rrdObj := range series.Values {
				value := float64(rrdObj.Value)
				if !math.IsNaN(value) {
					timestamp := rrdObj.Timestamp
					ticker := getNearestTicker(float64(timestamp), tickers)
					if len(ticker) > 0 {
						if _, ok := dataRaw[metric][ticker]; ok {
							dataRaw[metric][ticker] += value
						} else {
							dataRaw[metric][ticker] = value
						}
					}
				}
			}
			counts[metric]++
		}
	}
	for metric, series := range dataRaw {
		for _, ticker := range tickers {
			value := int(math.Floor(series[ticker]))
			date = strings.Split(ticker, " ")[0]
			ticker = strings.Split(ticker, " ")[1]
			data[metric][ticker] = value
		}
	}
	return data, date, counts
}

func getMean(values []int) int {
	mean := 0
	if len(values) == 0 {
		return mean
	}
	sum := 0
	for _, value := range values {
		sum += value
	}
	mean = sum / len(values)
	return mean
}

func getStandardDeviation(values []int) int {
	deviation := 0
	if len(values) == 0 {
		return deviation
	}
	total := 0
	mean := getMean(values)
	for _, value := range values {
		total += (value - mean) * (value - mean)
	}
	variance := float64(total) / float64(len(values))
	deviation = int(math.Sqrt(variance))
	return deviation
}

func getMinMaxAvg(values []int) (int, int, int) {
	avg := 0
	min := 0
	max := 0
	if len(values) > 0 {
		sum := 0
		for _, value := range values {
			sum += value
		}
		avg = sum / len(values)
		sort.Ints(values)
		min = values[0]
		max = values[len(values)-1]
	}
	return min, max, avg
}

func writeToDeviationsTable(platformName string, hour int, minute int, date string, ticker string) {
	o := orm.NewOrm()
	o.Using("apollo")
	var rows []orm.Params
	dateFull := date + " " + ticker + ":00"
	sql := "SELECT metric, COUNT(DISTINCT date), AVG(bits), STD(bits) "
	sql += "FROM `apollo`.`net` WHERE platform = ? AND hour = ? AND minute = ? "
	sql += "AND date >= DATE_SUB(?, INTERVAL 7 DAY) "
	sql += "AND date < DATE_SUB(?, INTERVAL 1 DAY) GROUP BY metric"
	num, err := o.Raw(sql, platformName, hour, minute, dateFull, dateFull).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		for _, row := range rows {
			samples := 0
			value, err := strconv.Atoi(row["COUNT(DISTINCT date)"].(string))
			if err != nil {
				log.Errorf(err.Error())
			} else {
				samples = value
			}
			if samples >= 3 {
				metricKey := 0
				value, err := strconv.Atoi(row["metric"].(string))
				if err != nil {
					log.Errorf(err.Error())
				} else {
					metricKey = value
				}
				mean := 0
				val, err := strconv.ParseFloat(row["AVG(bits)"].(string), 64)
				if err != nil {
					log.Errorf(err.Error())
				} else {
					mean = int(math.Floor(val))
				}
				deviation := 0
				val, err = strconv.ParseFloat(row["STD(bits)"].(string), 64)
				if err != nil {
					log.Errorf(err.Error())
				} else {
					deviation = int(math.Floor(val))
				}
				sql = "SELECT id FROM `apollo`.`deviations` WHERE date = ? AND platform = ? AND metric = ? LIMIT 1"
				num, err = o.Raw(sql, date+" "+ticker, platformName, metricKey).Values(&rows)
				if err != nil {
					log.Errorf(err.Error())
				} else if num == 0 {
					sql = "INSERT INTO `apollo`.`deviations`(`date`, `platform`, `metric`,"
					sql += "`samples`, `mean`, `deviation`, `updated`) VALUES("
					sql += "?, ?, ?, ?, ?, ?, ?)"
					_, err := o.Raw(sql, date+" "+ticker, platformName, metricKey, samples, mean, deviation,
						getNow()).Exec()
					if err != nil {
						log.Errorf(err.Error())
					}
				}
			}
		}
	}
}

func syncDeviationsTable() {
	platformNames := []string{}
	updatedPlatforms := map[string]map[string]string{}
	o := orm.NewOrm()
	o.Using("apollo")
	bo := NewBossOrm()
	var rows []orm.Params
	sql := "SELECT updated FROM `apollo`.`deviations` ORDER BY updated DESC LIMIT 1"
	num, err := o.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		format := "2006-01-02 15:04:05"
		updatedTime, _ := time.Parse(format, rows[0]["updated"].(string))
		currentTime, _ := time.Parse(format, getNow())
		diff := currentTime.Unix() - updatedTime.Unix()
		if int(diff) < g.Config().Contacts.Interval {
			return
		}
	}
	sql = "SELECT platform, principal FROM `boss`.`platforms` WHERE type LIKE '%业务' AND visible = 1 AND count > 0 ORDER BY platform ASC"
	num, err = bo.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		for _, row := range rows {
			platformName := row["platform"].(string)
			updatedPlatforms[platformName] = map[string]string{
				"contact": row["principal"].(string),
			}
			platformNames = append(platformNames, platformName)
		}
	}
	hours := []int{}
	for hour := 0; hour < 24; hour++ {
		hours = append(hours, hour)
	}
	minutes := []int{0, 20, 40}
	for _, platformName := range platformNames {
		for i := 0; i < 30; i++ {
			offset := i * (-1)
			date := time.Now().AddDate(0, 0, offset).Format("2006-01-02")
			dateFull := date + " 00:00:00"
			sql = "SELECT DISTINCT date FROM `apollo`.`net` "
			sql += "WHERE platform = ? AND hour = ? AND minute = ? "
			sql += "AND date >= DATE_SUB(?, INTERVAL 7 DAY) "
			sql += "AND date < DATE_SUB(?, INTERVAL 1 DAY) ORDER BY date DESC"
			num, err = o.Raw(sql, platformName, 0, 0, dateFull, dateFull).Values(&rows)
			if err != nil {
				log.Errorf(err.Error())
				break
			} else if num > 1 {
				for _, hour := range hours {
					for _, minute := range minutes {
						ticker := strconv.Itoa(hour) + ":"
						if hour < 10 {
							ticker = "0" + ticker
						}
						if minute == 0 {
							ticker += "00"
						} else {
							ticker += strconv.Itoa(minute)
						}
						dateQuery := date + " " + ticker + "%"
						sql = "SELECT date FROM `apollo`.`deviations` WHERE platform = ? AND date LIKE ? LIMIT 1"
						num, err = o.Raw(sql, platformName, dateQuery).Values(&rows)
						if err != nil {
							log.Errorf(err.Error())
						} else if num == 0 {
							writeToDeviationsTable(platformName, hour, minute, date, ticker)
						}
					}
				}
			} else {
				break
			}
		}
	}
}

func writeToNetTable(platformName string, offset int) {
	hours := []int{}
	for hour := 0; hour < 24; hour++ {
		hours = append(hours, hour)
	}
	minutes := []int{0, 20, 40}
	o := orm.NewOrm()
	o.Using("apollo")
	var rows []orm.Params
	data, date, counts := getPlatformsDailyTrafficData(platformName, offset)
	metrics := []string{
		"in",
		"out",
	}
	for metricKey, metric := range metrics {
		for _, hour := range hours {
			for _, minute := range minutes {
				ticker := strconv.Itoa(hour) + ":"
				if hour < 10 {
					ticker = "0" + ticker
				}
				if minute == 0 {
					ticker += "00"
				} else {
					ticker += strconv.Itoa(minute)
				}
				bits := 0
				if val, ok := data[metric][ticker]; ok {
					bits = val
				}
				sql := "SELECT id, date, hour, minute, platform, metric, count FROM `apollo`.`net` "
				sql += "WHERE date = ? AND hour = ? AND minute = ? AND platform = ? AND metric = ? LIMIT 1"
				num, err := o.Raw(sql, date, hour, minute, platformName, metricKey).Values(&rows)
				if err != nil {
					log.Errorf(err.Error())
				} else if num == 0 {
					sql = "INSERT INTO `apollo`.`net`(`date`, `hour`, `minute`,"
					sql += "`platform`, `metric`, `count`, `bits`,"
					sql += "`updated`) VALUES("
					sql += "?, ?, ?, ?, ?, ?, ?, ?)"
					_, err := o.Raw(sql, date, hour, minute, platformName,
						metricKey, counts[metric], bits,
						getNow()).Exec()
					if err != nil {
						log.Errorf(err.Error())
					}
				} else if num > 0 {
					count, _ := strconv.Atoi(rows[0]["count"].(string))
					if count < counts[metric] {
						ID := rows[0]["id"]
						sql := "UPDATE `apollo`.`net`"
						sql += " SET `date` = ?, `hour` = ?, `minute` = ?,"
						sql += " `platform` = ?, `metric` = ?, `count` = ?,"
						sql += " `bits` = ?, `updated` = ?"
						sql += " WHERE id = ?"
						_, err := o.Raw(sql, date, hour, minute, platformName,
							metricKey, counts[metric], bits,
							getNow(), ID).Exec()
						if err != nil {
							log.Errorf(err.Error())
						}
					}
				}
			}
		}
	}
}

func syncNetTable() {
	o := orm.NewOrm()
	o.Using("apollo")
	bo := NewBossOrm()
	var rows []orm.Params
	sql := "SELECT updated FROM `apollo`.`net` ORDER BY updated DESC LIMIT 1"
	num, err := o.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		format := "2006-01-02 15:04:05"
		updatedTime, _ := time.Parse(format, rows[0]["updated"].(string))
		currentTime, _ := time.Parse(format, getNow())
		diff := currentTime.Unix() - updatedTime.Unix()
		if int(diff) < g.Config().Contacts.Interval {
			return
		}
	}
	platformNames := []string{}
	sql = "SELECT platform, count FROM `boss`.`platforms` WHERE type LIKE '%业务' AND visible = 1 AND count > 0 ORDER BY platform ASC"
	num, err = bo.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		for _, row := range rows {
			platformName := row["platform"].(string)
			platformNames = append(platformNames, platformName)
		}
	}
	for _, platformName := range platformNames {
		for i := 1; i < 7; i++ {
			hostCountOfData := 0
			offset := i * (-1)
			date := time.Now().AddDate(0, 0, offset).Format("2006-01-02")
			sql = "SELECT MIN(count) FROM `apollo`.`net` "
			sql += "WHERE platform = ? AND date LIKE ?"
			num, err = o.Raw(sql, platformName, date+"%").Values(&rows)
			if err != nil {
				log.Errorf(err.Error())
			} else if num > 0 {
				if val, ok := rows[0]["MIN(count)"]; ok {
					if val != nil {
						value, err := strconv.Atoi(val.(string))
						if err == nil {
							hostCountOfData = value
						}
					}
				}
			}
			if hostCountOfData == 0 {
				writeToNetTable(platformName, i)
			} else {
				hostCountOfPlatform := 0
				sql = "SELECT DISTINCT hostname FROM `boss`.`ips` "
				sql += "WHERE platform = ? AND exist = 1 ORDER BY hostname ASC"
				num, err = bo.Raw(sql, platformName).Values(&rows)
				if err != nil {
					log.Errorf(err.Error())
				} else {
					hostCountOfPlatform = int(num)
				}
				if (hostCountOfData < hostCountOfPlatform) && (hostCountOfPlatform > 0) {
					writeToNetTable(platformName, i)
				}
			}
		}
	}
}

func syncHostData() {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("[syncHostData] has error: %v", r)
		}
	}()

	/**
	 * Lastest time of updated data on "ips"
	 * Checks interval(seconds)
	 */
	now := time.Now()
	intervalSeconds := g.Config().Hosts.Interval
	log.Infof("[Refresh \"ips, hosts, platforms\"] Current time: [%s]. Interval: %d seconds", now, intervalSeconds)
	if !isElapsedTimePassedForIpsTable(now, intervalSeconds) {
		log.Debugf("Skip synchronization")
		return
	}
	// :~)

	/**
	 * Loads IP data of platforms
	 */
	var ipDataOfPlatforms = make(map[string]interface{})
	errors := []string{}
	var result = make(map[string]interface{})
	result["error"] = errors

	loadIpDataOfPlatforms(ipDataOfPlatforms, result)

	if ipDataOfPlatforms["status"] == nil {
		return
	} else if int(ipDataOfPlatforms["status"].(float64)) != 1 {
		return
	}
	// :~)

	updatedPlatforms := map[string]bool{}
	hostname := ""
	hostData := map[string]map[string]string{}
	ipData := map[string]map[string]string{}

	ipListOfPlatforms := ipDataOfPlatforms["result"].([]interface{})

	log.Infof("Number of platforms: %d", len(ipListOfPlatforms))

	// Iterates every platform
	for _, platform := range ipListOfPlatforms {
		platformName := platform.(map[string]interface{})["platform"].(string)

		ipList := platform.(map[string]interface{})["ip_list"].([]interface{})

		log.Debugf("Platform[%s]. Number of IPs: [%d]", platformName, len(ipList))

		// Iterates every ip_list of every platform
		for _, device := range ipList {
			log.Debugf("Current ip(device): %#v", device)

			hostname = device.(map[string]interface{})["hostname"].(string)
			ipAddress := device.(map[string]interface{})["ip"].(string)
			status := device.(map[string]interface{})["ip_status"].(string)
			ipType := device.(map[string]interface{})["ip_type"].(string)
			item := map[string]string{
				"IP":       ipAddress,
				"status":   status,
				"hostname": hostname,
				"platform": platformName,
				"type":     strings.ToLower(ipType),
			}
			ipKeyOfPlatform := platformName + "_" + ipAddress
			if _, ok := ipData[ipAddress]; !ok {
				ipData[ipKeyOfPlatform] = item
			}

			if len(hostname) == 0 {
				continue
			}

			effectiveIpAddress := getIpFromHostnameWithDefault(hostname, ipAddress)

			if host, ok := hostData[hostname]; !ok {
				idcID := device.(map[string]interface{})["pop_id"].(string)
				host := map[string]string{
					"hostname":  hostname,
					"activate":  "0",
					"platforms": "",
					"idcID":     idcID,
					"IP":        ipAddress,
				}
				if len(effectiveIpAddress) > 0 {
					host["ipAddress"] = effectiveIpAddress
					host["platform"] = platformName
					platforms := []string{}
					if len(host["platforms"]) > 0 {
						platforms = strings.Split(host["platforms"], ",")
					}
					platforms = appendUniqueString(platforms, platformName)
					host["platforms"] = strings.Join(platforms, ",")
				}
				if status == "1" {
					host["activate"] = "1"
				}
				hostData[hostname] = host
			} else {
				if len(effectiveIpAddress) > 0 {
					host["ipAddress"] = effectiveIpAddress
					host["platform"] = platformName
					platforms := []string{}
					if len(host["platforms"]) > 0 {
						platforms = strings.Split(host["platforms"], ",")
					}
					platforms = appendUniqueString(platforms, platformName)
					host["platforms"] = strings.Join(platforms, ",")
				}
				if status == "1" {
					host["activate"] = "1"
				}
				hostData[hostname] = host
			}
		}

		updatedPlatforms[platformName] = true
	}

	updateIpsTable(ipData)
	updateHostsTable(hostData)
	detailOfPlatforms := loadDetailOfMatchedPlatforms(updatedPlatforms)
	updatePlatformsTable(detailOfPlatforms)
}

func syncContactsTable() {
	log.Debugf("func syncContactsTable()")
	o := NewBossOrm()
	var rows []orm.Params
	sql := "SELECT updated FROM `boss`.`contacts` ORDER BY updated DESC LIMIT 1"
	num, err := o.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		format := "2006-01-02 15:04:05"
		updatedTime, _ := time.Parse(format, rows[0]["updated"].(string))
		currentTime, _ := time.Parse(format, getNow())
		diff := currentTime.Unix() - updatedTime.Unix()
		if int(diff) < g.Config().Contacts.Interval {
			return
		}
	}
	platformNames := []string{}
	sql = "SELECT DISTINCT platform FROM boss.platforms ORDER BY platform ASC"
	num, err = o.Raw(sql).Values(&rows)
	if err != nil {
		log.Errorf(err.Error())
		return
	} else if num > 0 {
		for _, row := range rows {
			platformNames = append(platformNames, row["platform"].(string))
		}
	}

	var nodes = make(map[string]interface{})
	errors := []string{}
	var result = make(map[string]interface{})
	result["error"] = errors
	getPlatformContact(strings.Join(platformNames, ","), nodes)
	contactNames := []string{}
	contactsMap := map[string]map[string]string{}
	contacts := nodes["result"].(map[string]interface{})["items"].(map[string]interface{})
	for _, platformName := range platformNames {
		if items, ok := contacts[platformName]; ok {
			for _, user := range items.(map[string]map[string]string) {
				contactName := user["name"]
				if _, ok := contactsMap[contactName]; !ok {
					contactsMap[contactName] = user
					contactNames = append(contactNames, contactName)
				}
			}
		}
	}
	sort.Strings(contactNames)
	updateContactsTable(contactNames, contactsMap)
	addContactsToPlatformsTable(contacts)
}

func addContactsToPlatformsTable(contacts map[string]interface{}) {
	log.Debugf("func addContactsToPlatformsTable()")
	now := getNow()
	o := NewBossOrm()
	var platforms []Platforms
	_, err := o.QueryTable("platforms").All(&platforms)
	if err != nil {
		log.Errorf(err.Error())
	} else {
		for _, platform := range platforms {
			platformName := platform.Platform
			if items, ok := contacts[platformName]; ok {
				contacts := []string{}
				for role, user := range items.(map[string]map[string]string) {
					if role == "principal" {
						platform.Principal = user["name"]
					} else if role == "deputy" {
						platform.Deputy = user["name"]
					} else if role == "upgrader" {
						platform.Upgrader = user["name"]
					}
				}
				if len(platform.Principal) > 0 {
					contacts = append(contacts, platform.Principal)
				}
				if len(platform.Deputy) > 0 {
					contacts = append(contacts, platform.Deputy)
				}
				if len(platform.Upgrader) > 0 {
					contacts = append(contacts, platform.Upgrader)
				}
				platform.Contacts = strings.Join(contacts, ",")
			}
			platform.Updated = now
			_, err := o.Update(&platform)
			if err != nil {
				log.Errorf(err.Error())
			}
		}
	}
}

func updateContactsTable(contactNames []string, contactsMap map[string]map[string]string) {
	log.Debugf("func updateContactsTable()")
	o := NewBossOrm()
	var contact Contacts
	for _, contactName := range contactNames {
		user := contactsMap[contactName]
		err := o.QueryTable("contacts").Filter("name", user["name"]).One(&contact)
		if err == orm.ErrNoRows {
			sql := "INSERT INTO `boss`.`contacts`(name, phone, email, updated) VALUES(?, ?, ?, ?)"
			_, err := o.Raw(sql, user["name"], user["phone"], user["email"], getNow()).Exec()
			if err != nil {
				log.Errorf(err.Error())
			}
		} else if err != nil {
			log.Errorf(err.Error())
		} else {
			contact.Email = user["email"]
			contact.Phone = user["phone"]
			contact.Updated = getNow()
			_, err := o.Update(&contact)
			if err != nil {
				log.Errorf(err.Error())
			}
		}
	}
}

var insertIdcSql = `
INSERT INTO idcs(popid, idc, bandwidth, area, province, city, updated, count)
VALUES(
	:id, :name, :bandwidth, :area, :province, :city, :updated_time, 0
)
`
var updateIdcSql = `
UPDATE idcs
SET popid = :id, bandwidth = :bandwidth,
	area = :area, province = :province, city = :city,
	updated = :updated_time
WHERE idc = :name
`

func updateIdcData(idcData map[string]*sourceIdcRow) {
	const batchSize = 32

	log.Debugf("[Refresh \"idcs\"] Batch size: %d", batchSize)
	utils.MakeAbstractMap(idcData).SimpleBatchProcess(
		batchSize, (&txRefreshIdcsTable{time.Now()}).processBatch,
	)
}

type txRefreshIdcsTable struct {
	updateTime time.Time
}

func (self *txRefreshIdcsTable) processBatch(sourceData interface{}) {
	typedSource := sourceData.(map[string]*sourceIdcRow)

	txCallback := osqlx.TxCallbackFunc(
		func(tx *sqlx.Tx) db.TxFinale {
			txExt := osqlx.ToTxExt(tx)

			updateIdcStmt := txExt.PrepareNamed(updateIdcSql)
			insertIdcStmt := txExt.PrepareNamed(insertIdcSql)

			for _, idcRow := range typedSource {
				log.Debugf("Id [%v]. Name [%v].", idcRow.id, idcRow.name)
				sqlParams := map[string]interface{}{
					"id":           idcRow.id,
					"name":         idcRow.name,
					"bandwidth":    idcRow.bandwidth,
					"area":         idcRow.location.Area,
					"province":     idcRow.location.Province,
					"city":         idcRow.location.City,
					"updated_time": self.updateTime,
				}

				/**
				 * If the count of updated rows is 0,
				 * means there is no existing data for that IDC row.
				 */
				result := updateIdcStmt.MustExec(sqlParams)

				if db.ToResultExt(result).RowsAffected() == 0 {
					log.Debugf("The IDC is not existing, perform insertion")
					insertIdcStmt.MustExec(sqlParams)
				}
				// :~)
			}

			return db.TxCommit
		},
	)

	qdb.BossDbFacade.SqlxDbCtrl.InTx(txCallback)
}

const insertIpsSql = `
	INSERT INTO ips(ip, status, type, hostname, platform, updated, exist)
	VALUES(:ip, :status, :type, :hostname, :platform, :update_time, 1)
`
const updateIpsSql = `
	UPDATE ips
	SET hostname = :hostname, updated = :update_time,
		status = :status, type = :type, exist = 1
	WHERE ip = :ip AND platform = :platform
`
const turnOffExistToIpsSql = `
	UPDATE ips
	SET exist = 0, updated = FROM_UNIXTIME(?)
	WHERE exist = 1
		AND updated <= FROM_UNIXTIME(?) - INTERVAL 10 MINUTE
`

func updateIpsTable(IPsMap map[string]map[string]string) {
	now := time.Now()

	/**
	 * Checks time of interval on updating data
	 */
	if !isElapsedTimePassedForIpsTable(
		now, g.Config().Hosts.Interval,
	) {
		return
	}
	// :~)

	/**
	 * Insert or update data
	 */
	utils.MakeAbstractMap(IPsMap).SimpleBatchProcess(
		32,
		func(row interface{}) {
			ipData := row.(map[string]map[string]string)

			qdb.BossDbFacade.SqlxDbCtrl.InTx(osqlx.TxCallbackFunc(func(tx *sqlx.Tx) db.TxFinale {
				txExt := osqlx.ToTxExt(tx)

				insertStmt := txExt.PrepareNamed(insertIpsSql)
				updatedStmt := txExt.PrepareNamed(updateIpsSql)
				for _, row := range ipData {
					params := map[string]interface{}{
						"ip":          row["IP"],
						"status":      row["status"],
						"type":        row["type"],
						"hostname":    row["hostname"],
						"platform":    row["platform"],
						"update_time": now,
					}

					log.Debugf("[Insert/Update] ip param: %q", params)

					if db.ToResultExt(updatedStmt.MustExec(params)).RowsAffected() > 0 {
						continue
					}

					insertStmt.MustExec(params)
				}

				return db.TxCommit
			}))
		},
	)

	/**
	 * Turns off the exist for ips which are updated at least 10 minutes ago
	 */
	qdb.BossDbFacade.SqlxDbCtrl.
		Preparex(turnOffExistToIpsSql).
		MustExec(now.Unix(), now.Unix())
	// :~)
}

const insertHostsSql = `
	INSERT INTO hosts(
		exist,
		ip, hostname, platform, platforms,
		activate, isp, updated,
		idc, province, city
	)
	SELECT direct_columns.*,
		idc_columns.*
	FROM
		(
			SELECT
				1 AS d_exist,
				:ip AS d_ip, :hostname AS d_hostname, :platform AS d_platform, :platforms AS d_platforms,
				:activate AS d_activate, :isp AS d_isp, :update_time AS d_update_time
		) AS direct_columns
		LEFT OUTER JOIN
		(
			SELECT idc AS idc_name, province AS idc_province, city AS idc_city
			FROM idcs
			WHERE popid = :idc_id

		) AS idc_columns
		ON 1 = 1
`
const updateHostsSql = `
	UPDATE hosts AS hs
		LEFT OUTER JOIN
		(
			SELECT idc AS idc_name, province AS idc_province, city AS idc_city
			FROM idcs
			WHERE popid = :idc_id
		) AS idc
		ON 1 = 1
	SET hs.exist = 1,
		hs.ip = :ip, hs.activate = :activate,
		hs.platform = :platform, hs.platforms = :platforms,
		hs.isp = :isp, hs.updated = :update_time,
		hs.idc = idc.idc_name, hs.province = idc.idc_province, hs.city = idc.idc_city
	WHERE hs.hostname = :hostname
`
const turnOffExistToHostsSql = `
	UPDATE hosts
	SET exist = 0, updated = FROM_UNIXTIME(?)
	WHERE exist = 1
		AND updated <= FROM_UNIXTIME(?) - INTERVAL 10 MINUTE
`
const updateCountOfIdc = `
	UPDATE idcs
		LEFT OUTER JOIN
		(
			SELECT idc, COUNT(*) AS count_hosts
			FROM hosts
			GROUP BY idc
		) AS hs
		ON idcs.idc = hs.idc
	SET idcs.count = IFNULL(hs.count_hosts, 0)
`

func updateHostsTable(hostData map[string]map[string]string) {
	now := time.Now()

	/**
	 * Checks the interval of synchronization
	 */
	intervalSeconds := g.Config().Hosts.Interval
	log.Debugf("[Refresh \"hosts\"] Current time: [%s]. Interval: %d seconds", now, intervalSeconds)
	if !isElapsedTimePassedForHostsTable(now, intervalSeconds) {
		return
	}
	// :~)

	hosts := []map[string]string{}
	for _, host := range hostData {
		if len(host["platform"]) == 0 {
			host["platform"] = strings.Split(host["platforms"], ",")[0]
		}

		host["ISP"] = getIspFromHostname(host["hostname"])
		hosts = append(hosts, host)
	}

	/**
	 * Insert or update data
	 */
	utils.MakeAbstractArray(hosts).SimpleBatchProcess(
		32,
		func(batchData interface{}) {
			hostData := batchData.([]map[string]string)

			qdb.BossDbFacade.SqlxDbCtrl.InTx(osqlx.TxCallbackFunc(func(tx *sqlx.Tx) db.TxFinale {
				txExt := osqlx.ToTxExt(tx)

				insertStmt := txExt.PrepareNamed(insertHostsSql)
				updateStmt := txExt.PrepareNamed(updateHostsSql)

				for _, host := range hostData {
					params := map[string]interface{}{
						"ip": host["IP"], "hostname": host["hostname"],
						"platform": host["platform"], "platforms": host["platforms"],
						"isp": host["ISP"], "activate": host["activate"], "update_time": now,
						"idc_id": host["idcID"],
					}

					log.Debugf("[Insert/Update] Host params: %q", params)

					if result := db.ToResultExt(updateStmt.MustExec(params)); result.RowsAffected() > 0 {
						continue
					}

					insertStmt.MustExec(params)
				}

				return db.TxCommit
			}))
		},
	)
	// :~)

	/**
	 * Turns off the exist for hosts which are updated at least 10 minutes age
	 */
	qdb.BossDbFacade.SqlxDb.MustExec(
		turnOffExistToHostsSql,
		now.Unix(), now.Unix(),
	)
	// :~)

	qdb.BossDbFacade.SqlxDb.MustExec(
		updateCountOfIdc,
	)
}

var insertPlatformSql = `
INSERT INTO platforms(platform, type, department, team, visible, description, updated, count)
VALUES(
	:name, :type, :department, :team, :visible, :description, :updated_time,
	(
		SELECT COUNT(DISTINCT hostname)
		FROM ips
		WHERE platform = :name AND exist = 1
	)
)
`
var updatePlatformSql = `
UPDATE platforms
SET type = :type, department = :department, team = :team,
	visible = :visible, description = :description,
	updated = :updated_time,
	count = (
		SELECT COUNT(DISTINCT hostname)
		FROM ips
		WHERE platform = :name AND exist = 1
	)
WHERE platform = :name
`

func updatePlatformsTable(platforms []*bmodel.PlatformDetail) {
	now := time.Now()

	utils.MakeAbstractArray(platforms).SimpleBatchProcess(
		32,
		func(batchData interface{}) {
			platformData := batchData.([]*bmodel.PlatformDetail)

			qdb.BossDbFacade.SqlxDbCtrl.InTx(osqlx.TxCallbackFunc(func(tx *sqlx.Tx) db.TxFinale {
				txExt := osqlx.ToTxExt(tx)

				inserteStmt := txExt.PrepareNamed(insertPlatformSql)
				updateStmt := txExt.PrepareNamed(updatePlatformSql)

				for _, platformRow := range platformData {
					params := map[string]interface{}{
						"name":         platformRow.Name,
						"type":         platformRow.Type,
						"department":   platformRow.Department,
						"team":         platformRow.Team,
						"visible":      platformRow.Visible,
						"description":  platformRow.ShortenDescription(),
						"updated_time": now,
					}

					log.Debugf("[Insert/Update] platform param: %q", params)

					if db.ToResultExt(updateStmt.MustExec(params)).RowsAffected() > 0 {
						continue
					}

					inserteStmt.MustExec(params)
				}

				return db.TxCommit
			}))
		},
	)
}

func isElapsedTimePassedForIdcsTable(checkedTime time.Time, seconds int) bool {
	return isElapsedTimePassed(
		"idcs", "updated", checkedTime, seconds,
	)
}
func isElapsedTimePassedForIpsTable(checkedTime time.Time, seconds int) bool {
	return isElapsedTimePassed(
		"ips", "updated", checkedTime, seconds,
	)
}
func isElapsedTimePassedForHostsTable(checkedTime time.Time, seconds int) bool {
	return isElapsedTimePassed(
		"hosts", "updated", checkedTime, seconds,
	)
}

func isElapsedTimePassed(tableName string, timeColumnName string, checkedTime time.Time, seconds int) bool {
	count := 0

	qdb.BossDbFacade.SqlxDbCtrl.Get(
		&count,
		fmt.Sprintf(`
			SELECT COUNT(*)
			FROM (
				SELECT MAX(%s) AS max_value
				FROM %s
			) AS last_update
			WHERE TIMESTAMPDIFF(SECOND, last_update.max_value, FROM_UNIXTIME(?)) <= ?
			`,
			timeColumnName, tableName,
		),
		checkedTime.Unix(), seconds,
	)

	return count == 0
}
