{
  description = "Oku Agent: Hyperliquid perps from the oku binary";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = if (self ? shortRev) then self.shortRev else "dev";

        go = pkgs.go_1_27;

        oku = pkgs.buildGoModule.override { inherit go; } {
          pname = "oku";
          inherit version;
          src = ./.;

          vendorHash = "sha256-y4oV161fqP+TO4uanuhacNLkiUjFfOrUAHVBaqWL0c8=";

          subPackages = [ "cmd/oku" ];

          ldflags = [ "-s" "-w" "-X main.version=${version}" ];

          # Tests may require network access or credentials, so skip them in the sandbox.
          doCheck = false;

          meta = with pkgs.lib; {
            description = "Oku command line interface";
            homepage = "https://github.com/oku-trade/oku-cli";
            mainProgram = "oku";
            platforms = platforms.unix;
          };
        };
      in
      {
        packages = {
          inherit oku;
          default = oku;
        };

        apps.default = flake-utils.lib.mkApp { drv = oku; };

        devShells.default = pkgs.mkShell {
          packages = [ go ] ++ (with pkgs; [
            gopls
            gotools
            go-tools
            gnumake
            git
          ]);
        };

        formatter = pkgs.nixpkgs-fmt;
      });
}
