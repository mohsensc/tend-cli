plugins {
    id("org.springframework.boot") version "3.1.0"
    alias(libs.plugins.kotlin.jvm)
    kotlin("plugin.spring") version "1.9.10"
}

dependencies {
    implementation(libs.okhttp)
    implementation(libs.okhttp.logging)
    implementation(platform("software.amazon.awssdk:bom:2.20.0"))
    implementation("com.google.guava:guava:32.1.2-jre")
    implementation("software.amazon.awssdk:s3")
    // implementation("com.example:commented-out:1.0.0")
    /*
    implementation("com.example:block-commented:1.0.0")
    */
    testImplementation(libs.junit.jupiter)
    testImplementation("io.mockk:mockk:1.13.8")
}

dependencyLocking {
    lockAllConfigurations()
}
