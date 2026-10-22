package billing

import "example.com/lib/money"

// Total sums amounts with example.com/lib/money.
func Total(xs ...money.Amount) money.Amount { return money.Sum(xs...) }
