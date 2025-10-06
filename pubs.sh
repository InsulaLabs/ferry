#!/bin/bash

# Check if number of iterations is provided
if [ $# -eq 0 ]; then
    echo "Usage: $0 <number_of_iterations>"
    exit 1
fi

n=$1

# Generate one random message for this execution
message=$(openssl rand -hex 4)

for ((i=0; i<n; i++)); do
    ferry events publish notifs "{\"type\":\"alert\",\"message\":\"$message\"}"
    sleep 0.05
done