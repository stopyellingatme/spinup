mod wire_gen;

fn main() {
    for id in registry::ids() {
        println!("{id}");
    }
}
