# rateprowler

rudimentary http rate limit tester

cli tool to help estimate the rate limits of http APIs
 
🚧 work in progress 👷

created for a one-off use case, please make it better

## features

- test multiple endpoints simultaneously defined in a json configuration file
- configuration options include endpoint url, maximum requests per interval,
  maximum total requests, proxy server, and error wait intervals
- delay sending requests using incrementing values when hitting errors
- log the time elapsed between failures and successes
- track time between successful and failed requests for each endpoint
- realtime reporting of successful/failed requests and requests-per-second for
  all endpoints

## maybe:
- TUI to view/control/create endpoints
