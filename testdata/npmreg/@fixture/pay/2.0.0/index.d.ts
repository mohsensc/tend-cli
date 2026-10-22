export interface PaymentIntent {
  id: string
  amount: number
  currency: string
}

export declare const paymentIntents: {
  create(params: { amount: number; currency: string; customer?: string }): Promise<PaymentIntent>
  retrieve(id: string): Promise<PaymentIntent>
}
