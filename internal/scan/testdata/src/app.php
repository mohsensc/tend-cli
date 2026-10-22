<?php

namespace App\Billing;

use Stripe\StripeClient;
use GuzzleHttp\Client as Http;
use function Acme\Util\format_money;
use Aws\{S3\S3Client, Sqs\SqsClient as Queue};

final class Charger
{
    private StripeClient $stripe;

    public function __construct(string $key)
    {
        $this->stripe = new StripeClient($key);
        $this->http = new Http(['base_uri' => 'https://api.example.com']);
    }

    public function charge(): void
    {
        $this->stripe->charges->create(['amount' => 2000]);
        $s3 = new S3Client([]);
        $s3->putObject(['Bucket' => 'b']);
        $other->putObject([]);
        $q = new \Aws\Sqs\SqsClient([]);
        \Stripe\Stripe::setApiKey('sk');
        echo format_money(5); # Stripe\Stripe::x()
        $this->http->request('DELETE', '/v1/orders/1');
        $x = <<<EOT
        Stripe\StripeClient in a heredoc
        EOT;
    }
}
?>
<p>it's done</p>
