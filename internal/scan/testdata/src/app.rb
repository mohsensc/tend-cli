require 'stripe'
require "aws-sdk-s3"
require_relative 'lib/helper'

=begin
Stripe::Charge.create(amount: 1) inside a block comment
=end

class Billing
  def initialize(key)
    Stripe.api_key = key
    @s3 = Aws::S3::Client.new(region: 'us-east-1')
  end

  def charge
    sql = <<~SQL
      select * from charges where note = 'Stripe::Refund.create'
    SQL
    Stripe::Charge.create(amount: 2000, currency: 'usd')
    @s3.put_object(bucket: 'b', key: 'k')
    customer = Stripe::Customer.retrieve('cus_1')
    customer&.delete
    words = %w[Stripe::Payout.create]
    Faraday.post("https://api.example.com/v1/events") # Stripe.x
  end
end
