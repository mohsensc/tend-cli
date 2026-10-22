package com.acme;

import com.stripe.Stripe;
import com.stripe.model.Charge;
import com.stripe.param.*;
import static com.stripe.net.ApiResource.GSON;
import static org.junit.Assert.assertEquals;
import java.net.http.HttpClient;
import java.net.URI;
import org.junit.Test;

public class App {
    private final HttpClient http = HttpClient.newHttpClient();

    @Test
    public void run() throws Exception {
        Stripe.apiKey = "sk_test";
        Charge charge = Charge.create(new ChargeCreateParams());
        String s = "Charge.retrieve is text";
        char q = '"';
        Charge.retrieve("ch_1");
        assertEquals(1, 1);
        GSON.toJson(charge);
        var req = java.net.http.HttpRequest.newBuilder()
            .uri(URI.create("https://api.stripe.com/v1/charges"))
            .build();
        /* Charge.list() */
        http.send(req, null);
        String body = """
            {"note": "Charge.update"}
            """;
    }
}
