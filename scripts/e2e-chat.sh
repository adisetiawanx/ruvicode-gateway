#!/bin/bash
# End-to-end chat completion test through the gateway with the test key.
KEY='rvcd_testkey0001testkey0001testkey00'
echo "=== non-streaming ==="
curl -s -D .e2e-headers.txt http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"Say hello in exactly 3 words"}],"max_tokens":2000}' \
  -o .e2e-body.json
echo "--- relevant response headers:"
grep -iE 'x-cost|x-ruvicode|x-ratelimit|HTTP' .e2e-headers.txt
echo "--- body (trimmed):"
head -c 700 .e2e-body.json
echo
echo "=== streaming ==="
curl -s -N --max-time 60 http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"user","content":"Count from 1 to 3"}],"max_tokens":2000}' \
  -o .e2e-stream.txt
echo "--- stream line count: $(wc -l < .e2e-stream.txt)"
echo "--- last 3 lines:"
tail -3 .e2e-stream.txt | cut -c1-300
echo "--- leak check (provider/surplus identifiers):"
grep -icE 'surplus|openrouter|venice|bankr|is_byok|cost_details|deepseek/' .e2e-stream.txt || echo "0 (clean)"
rm -f .e2e-headers.txt .e2e-body.json .e2e-stream.txt
