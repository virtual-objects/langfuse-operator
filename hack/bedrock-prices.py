#!/usr/bin/env python3
# Copyright 2026 The langfuse-operator Authors.
# SPDX-License-Identifier: Apache-2.0
"""Print Amazon Bedrock on-demand token prices for a model, per 1M tokens.

Uses the public AWS Price List API (no credentials needed). Example:

    hack/bedrock-prices.py "Claude Sonnet 4.6"
    hack/bedrock-prices.py "Claude Sonnet 4.6" --region eu-central-1

"Global" rows are the global.* inference profile; the other rows are regional
(geo) profiles such as us.* / eu.* / apac.*.
"""
import argparse
import json
import sys
import urllib.request

URL = "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonBedrockFoundationModels/current/{region}/index.json"


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("model", help='Model name as AWS lists it, e.g. "Claude Sonnet 4.6"')
    ap.add_argument("--region", default="us-east-1")
    args = ap.parse_args()

    req = urllib.request.Request(URL.format(region=args.region), headers={"X-From": "langfuse-operator-hack"})
    with urllib.request.urlopen(req, timeout=60) as r:
        data = json.load(r)

    rows = []
    known = set()
    for sku, product in data["products"].items():
        name = product.get("attributes", {}).get("model")
        if name:
            known.add(name)
        if name != args.model:
            continue
        for term in data["terms"]["OnDemand"].get(sku, {}).values():
            for dim in term["priceDimensions"].values():
                if dim["unit"] != "1M tokens" or "Batch" in dim["description"]:
                    continue
                rows.append((dim["description"].split("|")[-1].strip(), dim["pricePerUnit"]["USD"]))

    if not rows:
        close = sorted(k for k in known if args.model.split()[0].lower() in k.lower())
        print(f"No on-demand token prices for {args.model!r} in {args.region}.", file=sys.stderr)
        if close:
            print("Similar names: " + ", ".join(close[:20]), file=sys.stderr)
        return 1
    for desc, usd in sorted(rows):
        print(f"{float(usd):>10.4f}  {desc}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
