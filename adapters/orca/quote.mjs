const MAX_INPUT_BYTES = 8192;
const MAX_INSTRUCTIONS = 16;
const MAX_ACCOUNTS_PER_INSTRUCTION = 64;
const MAX_DATA_BYTES = 512;
const TEMPORARY_PROVIDER_FAILURE = 75;
const WRAPPED_SOL_MINT = "So11111111111111111111111111111111111111112";
const DEVNET_USDC_MINT = "BRjpCHtyQLNCo8gqRUr8jtdAj5AjPYQaoqbvcZiHok1k";

function requireSupportedNode() {
  const parts = process.versions.node.split(".").map(Number);
  if (parts.length !== 3 || parts.some((part) => !Number.isInteger(part)) ||
      parts[0] !== 24 || parts[1] < 18) {
    throw new Error("unsupported Node.js runtime");
  }
}

function providerStatus(error) {
  return error?.context?.statusCode ?? error?.statusCode ?? error?.cause?.statusCode;
}

function isTemporaryProviderFailure(error) {
  const status = providerStatus(error);
  if (status === 429 || (Number.isInteger(status) && status >= 500 && status <= 599)) {
    return true;
  }
  const code = error?.cause?.code ?? error?.code;
  return ["ECONNRESET", "ECONNREFUSED", "EHOSTUNREACH", "ENETUNREACH", "ETIMEDOUT"].includes(code);
}

function fail(error) {
  // The message stays fixed for provider failures: an upstream error can carry
  // URLs, keys and account data, and this text reaches logs and the journal.
  // A LOCAL precondition is different — it contains nothing secret, and hiding
  // it left an operator on the wrong Node version reading "Orca quote failed"
  // with no hint that the runtime was the problem.
  const reason = error?.message === "unsupported Node.js runtime"
    ? `Orca quote failed: needs Node 24.18+ in the 24.x line, found ${process.versions.node}`
    : "Orca quote failed";
  process.stderr.write(reason + "\n");
  process.exitCode = isTemporaryProviderFailure(error) ? TEMPORARY_PROVIDER_FAILURE : 1;
}

function isPlainObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function parseInput(raw, parseAddress) {
  const input = JSON.parse(raw);
  const keys = Object.keys(input).sort();
  const expected = ["input_amount", "input_mint", "owner", "pool", "slippage_bps"];
  if (!isPlainObject(input) || JSON.stringify(keys) !== JSON.stringify(expected)) {
    throw new Error("invalid input shape");
  }
  if (!Number.isSafeInteger(input.slippage_bps) || input.slippage_bps < 1 || input.slippage_bps > 500) {
    throw new Error("invalid slippage");
  }
  if (!/^[1-9][0-9]{0,18}$/.test(input.input_amount)) {
    throw new Error("invalid amount");
  }
  return {
    owner: parseAddress(input.owner),
    pool: parseAddress(input.pool),
    inputMint: parseAddress(input.input_mint),
    inputAmount: BigInt(input.input_amount),
    slippageBps: input.slippage_bps,
  };
}

function nativeMintWrappingStrategy(inputMint) {
  const mint = String(inputMint);
  if (mint === WRAPPED_SOL_MINT) {
    return "ata";
  }
  if (mint === DEVNET_USDC_MINT) {
    return "seed";
  }
  throw new Error("unsupported input mint");
}

async function readInput() {
  const chunks = [];
  let size = 0;
  for await (const chunk of process.stdin) {
    size += chunk.length;
    if (size > MAX_INPUT_BYTES) {
      throw new Error("input too large");
    }
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function normalizeInstruction(instruction) {
  const accounts = instruction.accounts ?? [];
  const data = Buffer.from(instruction.data ?? []);
  if (accounts.length > MAX_ACCOUNTS_PER_INSTRUCTION || data.length > MAX_DATA_BYTES) {
    throw new Error("instruction exceeds limits");
  }
  return {
    program: instruction.programAddress,
    accounts: accounts.map((account) => ({
      address: account.address,
      signer: account.role >= 2,
      writable: account.role === 1 || account.role === 3,
    })),
    data_base64: data.toString("base64"),
  };
}

try {
  requireSupportedNode();
  const args = process.argv.slice(2);
  const [whirlpools, solanaKit] = await Promise.all([
    import("@orca-so/whirlpools"),
    import("@solana/kit"),
  ]);
  const {
    resetConfiguration,
    setNativeMintWrappingStrategy,
    swapInstructions,
    WhirlpoolDeployment,
  } = whirlpools;
  const {
    address,
    createNoopSigner,
    createSolanaRpc,
    devnet,
  } = solanaKit;
  if (args.length === 1 && args[0] === "--self-test") {
    const input = parseInput(JSON.stringify({
      owner: "11111111111111111111111111111111",
      pool: "11111111111111111111111111111111",
      input_mint: WRAPPED_SOL_MINT,
      input_amount: "1",
      slippage_bps: 1,
    }), address);
    let rejectedUnsupportedMint = false;
    try {
      nativeMintWrappingStrategy("11111111111111111111111111111111");
    } catch {
      rejectedUnsupportedMint = true;
    }
    try {
      setNativeMintWrappingStrategy(nativeMintWrappingStrategy(input.inputMint));
      const sellStrategyOK = whirlpools.NATIVE_MINT_WRAPPING_STRATEGY === "ata";
      setNativeMintWrappingStrategy(nativeMintWrappingStrategy(DEVNET_USDC_MINT));
      const buyStrategyOK = whirlpools.NATIVE_MINT_WRAPPING_STRATEGY === "seed";
      if (input.inputAmount !== 1n || input.slippageBps !== 1 ||
          !sellStrategyOK || !buyStrategyOK || !rejectedUnsupportedMint) {
        throw new Error("input self-test failed");
      }
    } finally {
      resetConfiguration();
    }
    process.stdout.write('{"status":"ok"}\n');
    process.exitCode = 0;
  } else {
    if (args.length !== 0) {
      throw new Error("invalid arguments");
    }
    const rpcURL = process.env.MITHRIL_AGENT_QUOTE_RPC_URL;
    const parsedURL = new URL(rpcURL);
    if (parsedURL.protocol !== "https:" || parsedURL.username || parsedURL.password || parsedURL.hash) {
      throw new Error("invalid RPC URL");
    }
    const rpc = createSolanaRpc(devnet(rpcURL));
    const input = parseInput(await readInput(), address);
    setNativeMintWrappingStrategy(nativeMintWrappingStrategy(input.inputMint));
    const signer = createNoopSigner(input.owner);
    const result = await swapInstructions(
      rpc,
      { inputAmount: input.inputAmount, mint: input.inputMint },
      input.pool,
      {
        signer,
        slippageToleranceBps: input.slippageBps,
        whirlpoolDeployment: WhirlpoolDeployment.devnet,
      },
    );
    if (result.instructions.length === 0 || result.instructions.length > MAX_INSTRUCTIONS) {
      throw new Error("invalid instruction count");
    }
    process.stdout.write(`${JSON.stringify({
      instructions: result.instructions.map(normalizeInstruction),
      token_in: result.quote.tokenIn.toString(),
      token_est_out: result.quote.tokenEstOut.toString(),
      token_min_out: result.quote.tokenMinOut.toString(),
      trade_enable_timestamp: result.tradeEnableTimestamp.toString(),
    })}\n`);
  }
} catch (error) {
  fail(error);
}
