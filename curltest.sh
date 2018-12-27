#!/bin/bash

# Run 10 iterations of 100 parallel curl connections
time for ((i=1;i<=20;i++)); do
  echo loop
  for n in `seq 100`; do
    (
    curl -ks --request POST --header "Content-Type: application/json" \
    --data "{\"time\":\"`date --rfc-3339=ns --utc`\",\"version\":\"0.1.0\"}" \
    --cacert server/ca.pem --key bacon.key --cert bacon.crt  https://localhost >/dev/null
    )&
  done

  wait
done
