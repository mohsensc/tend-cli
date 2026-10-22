package acme

import com.stripe.Stripe
import com.stripe.model.Customer as StripeCustomer
import okhttp3.*
import io.ktor.client.HttpClient

fun main() {
    Stripe.apiKey = "sk"
    val c = StripeCustomer.retrieve("cus_1")
    val client = HttpClient()
    client.close()
    val s = """
        Stripe.apiKey in a raw string
    """
    c?.delete()
}
