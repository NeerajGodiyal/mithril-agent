#![cfg_attr(any(target_os = "solana", target_arch = "bpf"), no_std)]

//! Atomic deployment pin for the two Jupiter routes accepted by mithril-agent.
//!
//! This program holds no state and signs for no account. The first account is
//! the pinned Jupiter ProgramData account; every remaining account is forwarded
//! unchanged to Jupiter. Passing ProgramData read-only makes an upgrade and a
//! guarded route mutually exclusive in the same bank execution.

use pinocchio::{
    account::AccountView,
    address::Address,
    cpi::invoke_with_slice,
    error::ProgramError,
    instruction::{InstructionAccount, InstructionView},
    ProgramResult,
};

#[cfg(any(target_os = "solana", target_arch = "bpf"))]
mod entrypoint {
    use super::{process_instruction, MAX_GUARD_ACCOUNTS};
    use pinocchio::{default_allocator, nostd_panic_handler, program_entrypoint};

    program_entrypoint!(process_instruction, MAX_GUARD_ACCOUNTS);
    default_allocator!();
    nostd_panic_handler!();
}

static JUPITER_PROGRAM: Address = Address::new_from_array([
    4, 121, 213, 91, 242, 49, 192, 110, 238, 116, 197, 110, 206, 104, 21, 7, 253, 177, 178, 222,
    163, 244, 142, 81, 2, 177, 205, 162, 86, 188, 19, 143,
]);
static JUPITER_PROGRAM_DATA: Address = Address::new_from_array([
    48, 15, 80, 96, 91, 190, 183, 48, 135, 137, 230, 192, 251, 227, 228, 163, 96, 32, 67, 111, 110,
    160, 202, 218, 128, 38, 13, 180, 111, 246, 96, 132,
]);
static JUPITER_UPGRADE_AUTHORITY: Address = Address::new_from_array([
    177, 30, 250, 215, 150, 143, 166, 116, 49, 40, 241, 115, 104, 101, 178, 164, 26, 28, 142, 191,
    135, 148, 41, 88, 182, 77, 232, 58, 131, 138, 47, 121,
]);
static UPGRADEABLE_LOADER: Address = Address::new_from_array([
    2, 168, 246, 145, 78, 136, 161, 176, 226, 16, 21, 62, 247, 99, 174, 43, 0, 194, 185, 61, 22,
    193, 36, 210, 192, 83, 122, 16, 4, 128, 0, 0,
]);
const JUPITER_DEPLOYMENT_SLOT: u64 = 441_316_428;

const ROUTE_V2: [u8; 8] = [187, 100, 250, 204, 49, 196, 175, 20];
const SHARED_ACCOUNTS_ROUTE_V2: [u8; 8] = [209, 152, 83, 147, 124, 254, 216, 233];
const MAX_CPI_ACCOUNTS: usize = 64;
// Parse one account past the accepted limit so oversized instructions cannot
// be truncated into an apparently valid route by the entrypoint.
#[cfg(any(target_os = "solana", target_arch = "bpf"))]
const MAX_GUARD_ACCOUNTS: usize = MAX_CPI_ACCOUNTS + 2;

const INVALID_INSTRUCTION: u32 = 1;
const INVALID_ACCOUNTS: u32 = 2;
const DEPLOYMENT_CHANGED: u32 = 3;

#[inline(never)]
pub fn process_instruction(
    _guard_program_id: &Address,
    accounts: &mut [AccountView],
    instruction_data: &[u8],
) -> ProgramResult {
    validate(accounts, instruction_data)?;

    let route_accounts = &accounts[1..];
    let mut metas: [InstructionAccount<'_>; MAX_CPI_ACCOUNTS] =
        core::array::from_fn(|_| InstructionAccount::readonly(&JUPITER_PROGRAM));
    for (meta, account) in metas.iter_mut().zip(route_accounts) {
        *meta = InstructionAccount::from(account);
    }
    invoke_with_slice(
        &InstructionView {
            program_id: &JUPITER_PROGRAM,
            data: instruction_data,
            accounts: &metas[..route_accounts.len()],
        },
        route_accounts,
    )
}

fn validate(accounts: &[AccountView], instruction_data: &[u8]) -> ProgramResult {
    let (program_index, user_index, minimum_accounts, minimum_data) =
        match instruction_data.get(..8) {
            Some(discriminator) if discriminator == ROUTE_V2 => (9, 0, 10, 35),
            Some(discriminator) if discriminator == SHARED_ACCOUNTS_ROUTE_V2 => (11, 1, 12, 36),
            _ => return Err(ProgramError::Custom(INVALID_INSTRUCTION)),
        };
    if instruction_data.len() < minimum_data
        || accounts.len() <= minimum_accounts
        || accounts.len() - 1 > MAX_CPI_ACCOUNTS
    {
        return Err(ProgramError::Custom(INVALID_ACCOUNTS));
    }

    let program_data = &accounts[0];
    let route_accounts = &accounts[1..];
    let jupiter_program = &route_accounts[program_index];
    if !route_accounts[user_index].is_signer()
        || route_accounts
            .iter()
            .enumerate()
            .any(|(index, account)| account.is_signer() && index != user_index)
    {
        return Err(ProgramError::Custom(INVALID_ACCOUNTS));
    }
    if program_data.address() != &JUPITER_PROGRAM_DATA
        || !program_data.owned_by(&UPGRADEABLE_LOADER)
        || program_data.executable()
        || program_data.is_signer()
        || program_data.is_writable()
        || jupiter_program.address() != &JUPITER_PROGRAM
        || !jupiter_program.owned_by(&UPGRADEABLE_LOADER)
        || !jupiter_program.executable()
        || jupiter_program.is_signer()
        || jupiter_program.is_writable()
    {
        return Err(ProgramError::Custom(DEPLOYMENT_CHANGED));
    }

    {
        let data = program_data.try_borrow()?;
        if data.len() < 45
            || data[..4] != 3u32.to_le_bytes()
            || data[4..12] != JUPITER_DEPLOYMENT_SLOT.to_le_bytes()
            || data[12] != 1
            || data[13..45] != JUPITER_UPGRADE_AUTHORITY.to_bytes()
        {
            return Err(ProgramError::Custom(DEPLOYMENT_CHANGED));
        }
    }
    {
        let data = jupiter_program.try_borrow()?;
        if data.len() != 36
            || data[..4] != 2u32.to_le_bytes()
            || data[4..36] != JUPITER_PROGRAM_DATA.to_bytes()
        {
            return Err(ProgramError::Custom(DEPLOYMENT_CHANGED));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    extern crate std;

    use super::*;
    use pinocchio::account::{RuntimeAccount, NOT_BORROWED};
    use std::{boxed::Box, vec, vec::Vec};

    #[repr(C)]
    struct TestAccount<const N: usize> {
        raw: RuntimeAccount,
        data: [u8; N],
    }

    fn account<const N: usize>(
        address: Address,
        owner: Address,
        signer: bool,
        writable: bool,
        executable: bool,
        data: [u8; N],
    ) -> AccountView {
        let stored = Box::leak(Box::new(TestAccount {
            raw: RuntimeAccount {
                borrow_state: NOT_BORROWED,
                is_signer: u8::from(signer),
                is_writable: u8::from(writable),
                executable: u8::from(executable),
                padding: [0; 4],
                address,
                owner,
                lamports: 0,
                data_len: N as u64,
            },
            data,
        }));
        // SAFETY: TestAccount is repr(C), so its fixed data immediately follows
        // the RuntimeAccount header and remains alive for the rest of the test.
        unsafe { AccountView::new_unchecked(&mut stored.raw) }
    }

    fn program_data(slot: u64, owner: Address, writable: bool) -> AccountView {
        let mut data = [0; 45];
        data[..4].copy_from_slice(&3u32.to_le_bytes());
        data[4..12].copy_from_slice(&slot.to_le_bytes());
        data[12] = 1;
        data[13..45].copy_from_slice(JUPITER_UPGRADE_AUTHORITY.as_ref());
        account(
            JUPITER_PROGRAM_DATA.clone(),
            owner,
            false,
            writable,
            false,
            data,
        )
    }

    fn jupiter_program(executable: bool, linked_data: Address) -> AccountView {
        let mut data = [0; 36];
        data[..4].copy_from_slice(&2u32.to_le_bytes());
        data[4..36].copy_from_slice(linked_data.as_ref());
        account(
            JUPITER_PROGRAM.clone(),
            UPGRADEABLE_LOADER.clone(),
            false,
            false,
            executable,
            data,
        )
    }

    fn route_fixture(
        shared: bool,
        slot: u64,
        program_data_owner: Address,
        program_data_writable: bool,
        jupiter_executable: bool,
        linked_data: Address,
        extra_signer: bool,
    ) -> (Vec<AccountView>, Vec<u8>) {
        let (count, user, program, discriminator) = if shared {
            (12, 1, 11, SHARED_ACCOUNTS_ROUTE_V2)
        } else {
            (10, 0, 9, ROUTE_V2)
        };
        let mut accounts = vec![program_data(
            slot,
            program_data_owner,
            program_data_writable,
        )];
        for index in 0..count {
            accounts.push(if index == program {
                jupiter_program(jupiter_executable, linked_data.clone())
            } else {
                account(
                    Address::new_from_array([index as u8 + 1; 32]),
                    Address::default(),
                    index == user || (extra_signer && index == count - 2),
                    index == 2,
                    false,
                    [],
                )
            });
        }
        let mut data = vec![0; if shared { 36 } else { 35 }];
        data[..8].copy_from_slice(&discriminator);
        (accounts, data)
    }

    fn valid_fixture(shared: bool) -> (Vec<AccountView>, Vec<u8>) {
        route_fixture(
            shared,
            JUPITER_DEPLOYMENT_SLOT,
            UPGRADEABLE_LOADER.clone(),
            false,
            true,
            JUPITER_PROGRAM_DATA.clone(),
            false,
        )
    }

    #[test]
    fn accepts_both_supported_routes() {
        for shared in [false, true] {
            let (accounts, data) = valid_fixture(shared);
            assert_eq!(validate(&accounts, &data), Ok(()));
        }
    }

    #[test]
    fn rejects_deployment_or_privilege_drift() {
        let cases = [
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT + 1,
                UPGRADEABLE_LOADER.clone(),
                false,
                true,
                JUPITER_PROGRAM_DATA.clone(),
                false,
            ),
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT,
                Address::default(),
                false,
                true,
                JUPITER_PROGRAM_DATA.clone(),
                false,
            ),
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT,
                UPGRADEABLE_LOADER.clone(),
                true,
                true,
                JUPITER_PROGRAM_DATA.clone(),
                false,
            ),
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT,
                UPGRADEABLE_LOADER.clone(),
                false,
                false,
                JUPITER_PROGRAM_DATA.clone(),
                false,
            ),
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT,
                UPGRADEABLE_LOADER.clone(),
                false,
                true,
                Address::default(),
                false,
            ),
            route_fixture(
                false,
                JUPITER_DEPLOYMENT_SLOT,
                UPGRADEABLE_LOADER.clone(),
                false,
                true,
                JUPITER_PROGRAM_DATA.clone(),
                true,
            ),
        ];
        for (accounts, data) in cases {
            assert!(validate(&accounts, &data).is_err());
        }
    }

    #[test]
    fn rejects_unknown_truncated_or_oversized_calls() {
        let (mut accounts, mut data) = valid_fixture(false);
        data[0] ^= 1;
        assert_eq!(
            validate(&accounts, &data),
            Err(ProgramError::Custom(INVALID_INSTRUCTION))
        );

        let (_, data) = valid_fixture(false);
        assert_eq!(
            validate(&accounts[..10], &data),
            Err(ProgramError::Custom(INVALID_ACCOUNTS))
        );

        for index in accounts.len() - 1..MAX_CPI_ACCOUNTS {
            accounts.push(account(
                Address::new_from_array([index as u8; 32]),
                Address::default(),
                false,
                false,
                false,
                [],
            ));
        }
        assert_eq!(accounts.len() - 1, MAX_CPI_ACCOUNTS);
        assert_eq!(validate(&accounts, &data), Ok(()));

        accounts.push(account(
            Address::new_from_array([255; 32]),
            Address::default(),
            false,
            false,
            false,
            [],
        ));
        assert_eq!(
            validate(&accounts, &data),
            Err(ProgramError::Custom(INVALID_ACCOUNTS))
        );
    }
}
