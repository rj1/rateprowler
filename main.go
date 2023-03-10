package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	_ "github.com/mattn/go-sqlite3"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

type Tester struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	Rate               string `json:"rate"`
	MaxRequests        int    `json:"maxRequests"`
	Proxy              string `json:"proxy"`
	ErrorWaitIntervals []int  `json:"errorWaitIntervals"`
}

type Result struct {
	Endpoint             string
	SuccessfulCount      int
	BatchSuccessfulCount int
	ErrorCount           int
	BatchErrorCount      int
	RequestsPerSecond    float64
	ErrorWait            time.Duration
	SleepStatus          bool
	TotalSuccessTime     time.Duration
}

func main() {
	configFile := flag.String("config", "config.json", "path to the configuration file")
	flag.Parse()

	if *configFile == "" {
		fmt.Println("please specify a configuration file using the -config flag")
		os.Exit(1)
	}

	config, err := loadConfig(*configFile)
	if err != nil {
		fmt.Printf("error loading configuration: %v\n", err)
		os.Exit(1)
	}

	db, err := databaseInit("rateprowler.db")

	var results []*Result
	var wg sync.WaitGroup

	for _, tester := range config.Testers {
		results = append(results, &Result{Endpoint: tester.URL})
	}

	startTime := time.Now()

	// report results every second
	go func() {
		for range time.Tick(time.Second) {
			for _, r := range results {
				fmt.Printf("[%s] %s: %d successful, %d errors, %.2f requests/s, sleeping: %t, error wait: %s\n", time.Now().Format("2006-01-02 15:04:05"),
					r.Endpoint, r.SuccessfulCount, r.ErrorCount, r.RequestsPerSecond, r.SleepStatus, r.ErrorWait.String())
			}
		}
	}()

	// start testers
	for i, tester := range config.Testers {
		wg.Add(100000)
		go func(i int, tester Tester) {
			defer wg.Done()

			transport := &http.Transport{
				TLSHandshakeTimeout: 10 * time.Second,
				MaxIdleConns:        0,
			}

			// set proxy if specified
			if tester.Proxy != "" {
				proxyURL, err := url.Parse(tester.Proxy)
				if err != nil {
					fmt.Printf("[%s] error parsing proxy: %s (%v)\n", tester.URL, tester.Proxy, err)
					return
				}
				transport.Proxy = http.ProxyURL(proxyURL)
			}

			// set up http client
			client := &http.Client{
				Timeout:   time.Second * 5,
				Transport: transport,
			}

			rate, err := parseRate(tester.Rate)
			if err != nil {
				fmt.Printf("[%s] error parsing rate value for endpoint: %s (%v)\n", tester.Name, tester.Rate, err)
				return
			}

			requestCount := 0
			waitIndex := 0
			errorWait := time.Duration(0)
			lastError := time.Time{}

			batchStartTime := time.Now()

			for requestCount < tester.MaxRequests {
				// wait for rate limit

				time.Sleep(rate.waitTime())

				// send request
				resp, err := client.Get(tester.URL)

				if err != nil || resp.StatusCode > 400 && resp.StatusCode < 500 {
					// handle failed request
					if err != nil {
						// log error to database
						db.Exec("INSERT INTO errors (name, type, error, timestamp) VALUES (?, ?, ?)", tester.Name, "sys", err, time.Now().Unix())
					} else {
						db.Exec("INSERT INTO errors (name, type, status, timestamp) VALUES (?, ?, ?, ?)", tester.Name, "http", resp.StatusCode, time.Now().Unix())
					}

					if requestCount == 0 {
						// tell the user that we're having errors on the first request
						fmt.Printf("[%s] error on first request to endpoint: %s\n", time.Now().Format("2006-01-02 15:04:05"), tester.Name)
					}

					results[i].ErrorCount++
					results[i].BatchErrorCount++

					// wait using error wait intervals
					if waitIndex < len(tester.ErrorWaitIntervals) {
						errorWait = time.Duration(tester.ErrorWaitIntervals[waitIndex]) * time.Second
						waitIndex++
					}

					results[i].TotalSuccessTime = time.Since(batchStartTime)
					batchStartTime = time.Now()

					// time of the last error
					if lastError.IsZero() {
						lastError = time.Now()
					}

					// sleep zzz
					results[i].SleepStatus = true
					time.Sleep(errorWait)

					// wake up aaa
					results[i].SleepStatus = false

				} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					// handle successful request
					results[i].SuccessfulCount++
					results[i].BatchSuccessfulCount++
					results[i].RequestsPerSecond = float64(results[i].SuccessfulCount) / time.Since(startTime).Seconds()

					// reset wait interval
					if waitIndex > 0 {
						waitIndex = 0
					}

					// log a success after we've experienced some failures
					if !lastError.IsZero() {
						endError := time.Now()

						// how long did this batch run for
						totalErrorWait := endError.Sub(lastError)

						// how long did it spit errors?
						results[i].ErrorWait = totalErrorWait

						// reset the last error time
						lastError = time.Time{}

						// log batch
						batch := Batch{
							Name:             tester.Name,
							Successes:        results[i].BatchSuccessfulCount,
							SuccessTime:      results[i].TotalSuccessTime,
							Failures:         results[i].BatchErrorCount,
							FailTime:         totalErrorWait,
							LastWaitInterval: tester.ErrorWaitIntervals[waitIndex],
						}

						logBatch(db, batch)
						results[i].BatchErrorCount = 0
						results[i].BatchSuccessfulCount = 0
					}
				}
				requestCount++
			}

		}(i, tester)
	}

	wg.Wait()
}

func loadConfig(filename string) (*struct{ Testers []Tester }, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config struct{ Testers []Tester }
	err = json.NewDecoder(file).Decode(&config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func parseRate(rate string) (*rateLimit, error) {
	var rl rateLimit

	if len(rate) < 2 {
		return nil, fmt.Errorf("invalid rate: %s", rate)
	}

	switch rate[len(rate)-1] {
	case 's':
		rl.interval = time.Second
	case 'm':
		rl.interval = time.Minute
	case 'h':
		rl.interval = time.Hour
	default:
		return nil, fmt.Errorf("invalid rate: %s", rate)
	}

	rateValue := rate[:len(rate)-1]

	fmt.Sscanf(rateValue, "%d", &rl.limit)

	if rl.limit <= 0 {
		return nil, fmt.Errorf("invalid rate: %s", rate)
	}

	return &rl, nil
}

type rateLimit struct {
	limit    int
	interval time.Duration
}

func (rl *rateLimit) waitTime() time.Duration {
	return rl.interval / time.Duration(rl.limit)
}

func databaseInit(dbname string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbname)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS log (
			id INTEGER PRIMARY KEY,
      name TEXT,
			successes INTEGER,
			success_time TEXT,
			failures INTEGER,
			fail_time TEXT,
      last_wait_interval INTEGER,
			timestamp INT
		)`)
	if err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	_, err = db.Exec(`
      CREATE TABLE IF NOT EXISTS errors (
      id INTEGER PRIMARY KEY,
      name TEXT,
      type TEXT,
      status INT,
      error TEXT,
      timestamp INT
    )`)
	if err != nil {
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	return db, nil
}

type Batch struct {
	Name             string
	Successes        int
	SuccessTime      time.Duration
	Failures         int
	FailTime         time.Duration
	LastWaitInterval int
}

func logBatch(db *sql.DB, batch Batch) error {
	// convert time.Duration to string
	successTime := batch.SuccessTime.String()
	failTime := batch.FailTime.String()
	_, err := db.Exec(`
		INSERT INTO log (name, successes, success_time, failures, fail_time, last_wait_interval, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, batch.Name, batch.Successes, successTime, batch.Failures, failTime, batch.LastWaitInterval, time.Now().Unix())
	if err != nil {
		fmt.Printf("failed to log request: %s", err)
	}

	return nil
}
