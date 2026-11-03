#!/usr/bin/env bash
# A small command line program that prints a table.
rows=("Notebook 3.50 12" "Pencil 0.80 40" "Eraser 0.45 25")

printf '%-10s %7s %5s %8s\n' Item Price Qty Total
for row in "${rows[@]}"; do
  read -r name price qty <<<"$row"
  printf '%-10s %7.2f %5d %8.2f\n' "$name" "$price" "$qty" "$(awk -v p="$price" -v q="$qty" 'BEGIN { print p * q }')"
done
