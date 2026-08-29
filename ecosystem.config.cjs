module.exports = {
    apps: [{
        name: "rfelogappwasap",
        script: "./rfelogappwasap",
        watch: false,
        instances: 1,
        exec_mode: "fork",
        env: {
            NODE_ENV: "production",
            PORT: 8090
        }
    }]
}