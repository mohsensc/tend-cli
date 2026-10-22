export interface Charge {
  id: string
  amount: number
  currency: string
}

export declare const charges: {
  create(params: { amount: number; currency: string; customer?: string }): Promise<Charge>
  retrieve(id: string): Promise<Charge>
}
