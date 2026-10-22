using System;
using System.Net.Http;
using Stripe;
using Stripe.Checkout;
using Json = Newtonsoft.Json.JsonConvert;
global using static System.Math;

namespace Acme
{
    public class Billing
    {
        private readonly HttpClient http = new HttpClient();

        public void Run()
        {
            StripeConfiguration.ApiKey = "sk";
            var service = new ChargeService();
            service.Create(new ChargeCreateOptions { Amount = 2000 });
            var path = @"C:\stripe\""ChargeService.Create""";
            var s = Json.SerializeObject(service);
            Console.WriteLine($"{s} Stripe.Charge in a string");
            var helper = new Billing();
            Stripe.Refund.Create();
            http.GetAsync("https://api.stripe.com/v1/charges");
        }
    }
}
