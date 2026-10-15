{
  description = "net-merge: use all your network adapters at once";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = self.shortRev or self.dirtyShortRev or "dev";
    in
    {
      packages = forAllSystems (pkgs: rec {
        net-merge = pkgs.callPackage ./nix/package.nix { inherit version; };
        default = net-merge;
      });

      nixosModules = rec {
        net-merge = import ./nix/module.nix self;
        default = net-merge;
      };

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go_1_27 ]; };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
