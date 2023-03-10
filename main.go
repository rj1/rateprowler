package main

import (
	"encoding/json"
	"flag"
	"fmt"
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
	Endpoint          string
	SuccessfulCount   int
	ErrorCount        int
	RequestsPerSecond float64
	ErrorWait         time.Duration
	SleepStatus       bool
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

			for requestCount < tester.MaxRequests {
				// wait for rate limit

				time.Sleep(rate.waitTime())

				// send request
				resp, err := client.Get(tester.URL)

				if err != nil || resp.StatusCode > 400 && resp.StatusCode < 500 {
					// handle failed request
					var errorMsg string
					if err != nil {
						errorMsg = fmt.Sprintf("[%s] - %s - %v\n", time.Now().Format("2006-01-02 15:04:05"), tester.URL, err)
					} else {
						errorMsg = fmt.Sprintf("[%s] - %s - %v\n", time.Now().Format("2006-01-02 15:04:05"), tester.URL, resp.StatusCode)
					}

					// log error to file
					f, err := os.OpenFile("error.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
					if err != nil {
						fmt.Println(err)
					}
					defer f.Close()

					if _, err := f.WriteString(errorMsg); err != nil {
						fmt.Println(err)
					}

					if requestCount == 0 {
						// tell the user that we're having errors on the first request
						fmt.Printf("[%s] error on first request to endpoint: %s\n", time.Now().Format("2006-01-02 15:04:05"), tester.Name)
					}

					results[i].ErrorCount++
					// wait using error wait intervals
					if waitIndex < len(tester.ErrorWaitIntervals) {
						errorWait = time.Duration(tester.ErrorWaitIntervals[waitIndex]) * time.Second
						waitIndex++
					}

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
					results[i].RequestsPerSecond = float64(results[i].SuccessfulCount) / time.Since(startTime).Seconds()

					// reset wait interval
					if waitIndex > 0 {
						waitIndex = 0
					}

					// log a success after we've experienced some failures
					if !lastError.IsZero() {
						endError := time.Now()

						// add the error wait to the results
						results[i].ErrorWait = endError.Sub(lastError)
						// reset the last error time
						lastError = time.Time{}
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
