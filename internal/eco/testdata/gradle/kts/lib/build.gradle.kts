val ktorVersion = "2.3.4"
extra["slf4jVersion"] = "2.0.9"

dependencies {
    api("io.ktor:ktor-client-core:$ktorVersion")
    implementation("org.slf4j:slf4j-api:${property("slf4jVersion")}")
    implementation("io.ktor:ktor-client-cio:${ktorVersion}")
    implementation(libs.gson)
}
