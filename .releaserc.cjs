// semantic-release configuration.
//
// Release rules are chosen by the version band of the latest `v*` tag:
//
//   0.x (or no tag yet) — initial development: every ordinary commit
//   (feat/fix/perf) ships as a patch. Only BREAKING CHANGE moves the minor,
//   the loudest signal left when minor no longer means "features", so a 0.x
//   series can never reach 1.0.0 by accident.
//
//   >= 1.0 — standard semver: feat → minor, BREAKING CHANGE → major.
//
// Crossing into 1.x is a deliberate act, not something CI decides: delete or
// flip `preOne` below and the next push releases under standard rules.
//
// The rules must be explicit because semantic-release has NO built-in 0.x
// handling — its defaults turn any breaking change into v1.0.0 (and a first
// release with no tags into 1.0.0 outright).
const { execSync } = require("node:child_process");

function lastTag() {
  try {
    return execSync("git tag --list 'v*' --sort=-v:refname", { encoding: "utf8" })
      .split("\n")[0]
      .trim();
  } catch {
    return "";
  }
}

const majorAt = Number.parseInt(lastTag().replace(/^v/, "").split(".")[0], 10);
const preOne = !Number.isFinite(majorAt) || majorAt === 0;

const parserOpts = {
  headerPattern: "^(\\w*)(?:\\((.*)\\))?!?: (.*)$",
  breakingHeaderPattern: "^(\\w*)(?:\\((.*)\\))?!: (.*)$",
};

module.exports = {
  branches: ["master"],
  tagFormat: "v${version}",
  plugins: [
    [
      "@semantic-release/commit-analyzer",
      {
        preset: "angular",
        releaseRules: preOne
          ? [
              { breaking: true, release: "minor" },
              { revert: true, release: "patch" },
              { type: "feat", release: "patch" },
              { type: "fix", release: "patch" },
              { type: "perf", release: "patch" },
            ]
          : [
              { breaking: true, release: "major" },
              { revert: true, release: "patch" },
              { type: "feat", release: "minor" },
              { type: "fix", release: "patch" },
              { type: "perf", release: "patch" },
            ],
        parserOpts,
      },
    ],
    ["@semantic-release/release-notes-generator", { preset: "angular", parserOpts }],
    // Rebuild inside prepare so the attached binaries carry the real version
    // (main.version) instead of the "dev" fallback from a plain go build.
    [
      "@semantic-release/exec",
      {
        prepareCmd:
          "go build -ldflags='-s -w -X main.version=${nextRelease.version}' -o gotomux ." +
          " && go build -ldflags='-s -w -X main.version=${nextRelease.version}' -o gotomuxd ./cmd/gotomuxd/",
      },
    ],
    ["@semantic-release/github", { assets: ["gotomux", "gotomuxd"] }],
  ],
};
